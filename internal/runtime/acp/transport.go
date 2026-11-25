package acp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/proc"
)

// Transport 是按行 JSON 的传输层。客户端与测试替身都实现它，
// 这样协议逻辑不需要认识 os/exec。
type Transport interface {
	WriteLine(line []byte) error
	ReadLine() ([]byte, error)
	Close() error
}

const maxLine = 8 << 20 // 8MB，远大于正常 token 流；防止对端发坏帧把内存打爆

// StreamTransport 在任意 io.Reader/Writer 上跑 NDJSON。
type StreamTransport struct {
	r      *bufio.Reader
	w      io.Writer
	closer io.Closer
	mu     sync.Mutex
	once   sync.Once
}

// NewStreamTransport 建一个流式传输。closer 可以为 nil。
func NewStreamTransport(r io.Reader, w io.Writer, closer io.Closer) *StreamTransport {
	return &StreamTransport{r: bufio.NewReaderSize(r, 64<<10), w: w, closer: closer}
}

// WriteLine 写一帧并补换行。多 goroutine 同时写会坏帧，所以必须加锁。
func (t *StreamTransport) WriteLine(line []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(line) == 0 {
		return errors.New("acp: empty frame")
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	_, err := t.w.Write(buf)
	return err
}

// ReadLine 读一帧。ReadBytes 不受 Scanner 64KB 上限影响；超长帧直接报错。
func (t *StreamTransport) ReadLine() ([]byte, error) {
	line, err := t.r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	line = []byte(strings.TrimRight(string(line), "\r\n"))
	if len(line) > maxLine {
		return nil, fmt.Errorf("acp: frame exceeds %d bytes", maxLine)
	}
	if err != nil {
		return line, nil
	}
	return line, nil
}

// Close 关闭底层资源。
func (t *StreamTransport) Close() error {
	var err error
	t.once.Do(func() {
		if t.closer != nil {
			err = t.closer.Close()
		}
	})
	return err
}

// Process 是一个 ACP 子进程。stdout 走协议，stderr 只留尾部若干字节用于排错。
//
// 读 stdout 和 Wait 在同一个 goroutine 里：exec.Cmd 的约定是「stdout 读完之后才能 Wait」，
// 否则进程刚退出时 Wait 会关掉管道，最后几帧响应可能丢。
type Process struct {
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stdin    io.WriteCloser
	stderr   *tailBuffer
	lines    chan lineItem
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	writeMu  sync.Mutex
	waitMu   sync.Mutex
	waitErr  error
}

type lineItem struct {
	data []byte
	err  error
}

// StartProcess 拉起 ACP Agent 进程。
//
// ctx 的生命周期就是进程生命周期：取消 ctx 会杀整个进程组（proc.Setup 已接好），
// 避免 CLI 派生的 node / git 孙进程继续持有 stdout。
func StartProcess(ctx context.Context, command string, args []string, dir string, env []string) (*Process, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("acp: empty command")
	}
	name, argv := commandFor(command, args)
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, name, argv...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	proc.Setup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	tail := &tailBuffer{limit: 8 << 10}
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	p := &Process{
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		stderr: tail,
		lines:  make(chan lineItem, 1024),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
	}
	go p.pump(stdout)
	return p, nil
}

// pump 持续读 stdout，读完再 Wait。通道有界，消费者慢时形成背压。
func (p *Process) pump(stdout io.ReadCloser) {
	r := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			line = []byte(strings.TrimRight(string(line), "\r\n"))
			if len(line) > maxLine {
				select {
				case p.lines <- lineItem{err: fmt.Errorf("acp: frame exceeds %d bytes", maxLine)}:
				case <-p.stop:
				}
				break
			}
			if len(line) > 0 {
				select {
				case p.lines <- lineItem{data: line}:
				case <-p.stop:
					goto wait
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				select {
				case p.lines <- lineItem{err: err}:
				case <-p.stop:
				}
			}
			break
		}
	}
wait:
	err := p.cmd.Wait()
	p.waitMu.Lock()
	p.waitErr = err
	p.waitMu.Unlock()
	close(p.done)
	close(p.lines)
}

// WriteLine 写一帧到 stdin。
func (p *Process) WriteLine(line []byte) error {
	if len(line) == 0 {
		return errors.New("acp: empty frame")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.stdin.Write(append(append([]byte{}, line...), '\n'))
	return err
}

// ReadLine 从 pump 的队列里取一帧。
func (p *Process) ReadLine() ([]byte, error) {
	it, ok := <-p.lines
	if !ok {
		p.waitMu.Lock()
		err := p.waitErr
		p.waitMu.Unlock()
		if err == nil {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("acp: process exited: %v (stderr: %s)", err, p.Stderr())
	}
	if it.err != nil {
		return nil, it.err
	}
	return it.data, nil
}

// Stderr 返回子进程 stderr 的尾部，用于启动失败时给用户看原因。
func (p *Process) Stderr() string {
	if p == nil || p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

// Wait 等进程退出并返回退出错误。
func (p *Process) Wait() error {
	if p == nil {
		return nil
	}
	<-p.done
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

// Close 取消 ctx（杀进程组），关闭 stdin，再等读循环退出。最多等 3 秒。
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		close(p.stop)
		p.cancel()
		_ = p.stdin.Close()
	})
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

// commandFor 处理 Windows 上 npm 安装的 .cmd/.bat 包装脚本。
// CreateProcess 不能直接执行批处理，必须经 cmd.exe /c。
func commandFor(name string, args []string) (string, []string) {
	if runtime.GOOS != "windows" {
		return name, args
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return name, args
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return "cmd.exe", append([]string{"/c", path}, args...)
	}
	return path, args
}

// tailBuffer 是并发安全的定长环形缓冲，只保留最后 limit 字节。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
