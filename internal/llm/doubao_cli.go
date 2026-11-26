package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/proc"
)

// 豆包 CLI 是一条独立通道：它自己有账号、模型目录和工具循环。
// 这里只做两件事：拉模型列表、把 -p 的输出翻成 Chunk。
//
// 为什么不用 HTTP：豆包 CLI 的账号体系（登录态、额度、模型目录）都在本机，
// 走 CLI 等于直接复用用户已经登录的那份能力，不需要再填 Key。
const (
	DoubaoCLIProvider = "doubao-cli"
	// DefaultDoubaoCLIModel 是目录拿不到时的兜底。目录正常时会被覆盖。
	DefaultDoubaoCLIModel = "person-deepseek-v4-1-flash-261017"
	// doubaoCatalogTTL 让模型目录不要每打开一次下拉框就重新拉一遍。
	doubaoCatalogTTL = 5 * time.Minute
)

// ErrDoubaoCLIMissing 表示本机没装豆包 CLI。
var ErrDoubaoCLIMissing = errors.New("没找到豆包 CLI：请先安装 @bytedance/doubao-cli，或设置 MAD_DOUBAO_BIN")

// DoubaoCLIRun 是一次非交互任务。
type DoubaoCLIRun struct {
	WorkDir string
	Prompt  string
	Model   string
	Effort  string
}

// doubaoBin 允许测试替换可执行文件。
var doubaoBin = findDoubaoBin

// doubaoCommand 允许测试换成自己的子进程。
var doubaoCommand = func(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

func findDoubaoBin() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MAD_DOUBAO_BIN")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%w（MAD_DOUBAO_BIN 指向的文件不存在）", ErrDoubaoCLIMissing)
	}
	if runtime.GOOS == "windows" {
		// npm 装的是 .cmd 壳，真正能直接执行的是这个原生入口。
		// 先找它，避免把提示词塞进 cmd.exe 的引号地狱。
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			p := filepath.Join(local, "doubao_cli", "app", "current", "doubao.exe")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath("doubao"); err == nil {
		if strings.HasSuffix(strings.ToLower(p), ".exe") {
			return p, nil
		}
		return "", fmt.Errorf("%w（找到了 %s，但它不是可执行文件；请设置 MAD_DOUBAO_BIN）", ErrDoubaoCLIMissing, p)
	}
	if p, err := exec.LookPath("doubao.exe"); err == nil {
		return p, nil
	}
	return "", ErrDoubaoCLIMissing
}

// DoubaoCLIStatus 报告本机 CLI 能不能用。设置页据此显示「已登录 / 没装」。
func DoubaoCLIStatus(ctx context.Context) (bool, string) {
	bin, err := doubaoBin()
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-v")
	proc.Setup(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("豆包 CLI 无法启动：%s", strings.TrimSpace(string(out)))
	}
	ver := strings.TrimSpace(string(out))
	if ver == "" {
		ver = "豆包 CLI"
	}
	return true, ver
}

type doubaoModelEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}

type doubaoCatalogSnap struct {
	at     time.Time
	models []ModelMeta
}

var (
	doubaoCatalogMu   sync.Mutex
	doubaoCatalogOnce doubaoCatalogSnap
)

// ListDoubaoCLIModels 跑 `doubao model list --json`，把模型和推理档位带回来。
func ListDoubaoCLIModels(ctx context.Context) ([]ModelMeta, error) {
	doubaoCatalogMu.Lock()
	snap := doubaoCatalogOnce
	doubaoCatalogMu.Unlock()
	if len(snap.models) > 0 && time.Since(snap.at) < doubaoCatalogTTL {
		return append([]ModelMeta(nil), snap.models...), nil
	}

	bin, err := doubaoBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "model", "list", "--json")
	proc.Setup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("读取豆包模型目录失败：%w", err)
	}
	models, err := ParseDoubaoCatalog(out)
	if err != nil {
		return nil, err
	}
	doubaoCatalogMu.Lock()
	doubaoCatalogOnce = doubaoCatalogSnap{at: time.Now(), models: models}
	doubaoCatalogMu.Unlock()
	return append([]ModelMeta(nil), models...), nil
}

// ParseDoubaoCatalog 从 CLI 的 stdout 里找 result 事件并解析模型表。
// 只看 JSON 行，忽略 [cli-chat] 之类的日志。
func ParseDoubaoCatalog(raw []byte) ([]ModelMeta, error) {
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type   string `json:"type"`
			Result struct {
				Models []doubaoModelEntry `json:"models"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || len(ev.Result.Models) == 0 {
			continue
		}
		models := make([]ModelMeta, 0, len(ev.Result.Models))
		for _, m := range ev.Result.Models {
			id := m.ID
			if id == "" {
				continue
			}
			meta := ModelMeta{Name: id, Reasoning: len(m.Efforts) > 0, Efforts: append([]string(nil), m.Efforts...)}
			models = append(models, meta)
		}
		if len(models) > 0 {
			return models, nil
		}
	}
	return nil, fmt.Errorf("豆包 CLI 没有返回模型目录")
}

// StreamDoubaoCLI 跑一次 `-p -`，把逐行事件翻成 Chunk。
// 提示词走 stdin，避免 Windows 命令行长度和引号问题。
func StreamDoubaoCLI(ctx context.Context, run DoubaoCLIRun) (<-chan Chunk, error) {
	bin, err := doubaoBin()
	if err != nil {
		return nil, err
	}
	args := []string{"-C", run.WorkDir, "-p", "-", "--output-format", "stream-json", "--permission-mode", "always_allow"}
	if run.Model != "" {
		args = append(args, "--model", run.Model)
	}
	if e := cliEffort(run.Effort); e != "" {
		args = append(args, "--effort", e)
	}
	cmd := doubaoCommand(ctx, bin, args...)
	proc.Setup(cmd)
	cmd.Stdin = strings.NewReader(run.Prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动豆包 CLI 失败：%w", err)
	}

	out := make(chan Chunk, 16)
	go func() {
		defer close(out)
		var errBuf strings.Builder
		go func() {
			buf := make([]byte, 4096)
			for {
				n, rerr := stderr.Read(buf)
				if n > 0 && errBuf.Len() < 2000 {
					errBuf.Write(buf[:n])
				}
				if rerr != nil {
					return
				}
			}
		}()

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		saw := false
		for sc.Scan() {
			line := sc.Text()
			for _, ev := range ParseDoubaoEvent(line) {
				saw = true
				if ev.Delta != "" {
					ev.Done = false
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					// 停止时杀掉整组，再让扫描自然读到 EOF；
					// 直接 return 会漏掉 Wait，留下没回收的进程。
					_ = cmd.Cancel()
				}
			}
		}
		werr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if !saw {
			msg := strings.TrimSpace(errBuf.String())
			if msg == "" {
				msg = "豆包 CLI 没有输出"
			}
			out <- Chunk{Err: msg}
			return
		}
		if werr != nil {
			msg := strings.TrimSpace(errBuf.String())
			if msg == "" {
				msg = fmt.Sprintf("豆包 CLI 退出：%v", werr)
			}
			out <- Chunk{Err: msg}
			return
		}
		out <- Chunk{Done: true}
	}()
	return out, nil
}

func cliEffort(e string) string {
	switch e {
	case "off", "none":
		return ""
	case "low", "medium", "high", "xhigh", "max":
		return e
	default:
		return ""
	}
}

// ParseDoubaoEvent 把一行 stream-json 翻成零到多个 Chunk。
//
// 同时兼容两类上游：完整消息事件（assistant / user / result）和
// 增量事件（stream_event + content_block_delta）。未知事件一律忽略，
// 不让一次格式升级把整轮打成错误。
func ParseDoubaoEvent(line string) []Chunk {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	// 日志行形如 [cli-chat] {"level":"info",...}，不是事件。
	if strings.HasPrefix(line, "[cli-chat]") {
		return nil
	}
	if !strings.HasPrefix(line, "{") {
		// 有的版本直接吐纯文本。
		return []Chunk{{Delta: line}}
	}
	var ev struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		IsError bool            `json:"is_error"`
		Result  json.RawMessage `json:"result"`
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Event *struct {
			Type  string `json:"type"`
			Delta *struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content_block"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}

	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			return chunksFromContent(ev.Message.Content)
		}
	case "user":
		// 工具结果：只报结束，不把原始输出倒进正文。
		if ev.Message != nil {
			if content := chunksFromContent(ev.Message.Content); len(content) > 0 {
				var out []Chunk
				for _, c := range content {
					if c.ToolPhase == "end" {
						out = append(out, c)
					}
				}
				return out
			}
		}
	case "stream_event":
		if ev.Event == nil {
			return nil
		}
		switch ev.Event.Type {
		case "content_block_delta":
			if ev.Event.Delta != nil {
				if ev.Event.Delta.Text != "" {
					return []Chunk{{Delta: ev.Event.Delta.Text}}
				}
				if ev.Event.Delta.Thinking != "" {
					return []Chunk{{Reasoning: ev.Event.Delta.Thinking}}
				}
			}
		case "content_block_start":
			if ev.Event.ContentBlock != nil && ev.Event.ContentBlock.Type == "tool_use" {
				return []Chunk{{Tool: ev.Event.ContentBlock.Name, ToolPhase: "start", CallID: ev.Event.ContentBlock.ID}}
			}
		}
	case "result":
		var text string
		if len(ev.Result) > 0 {
			if json.Unmarshal(ev.Result, &text) != nil {
				text = ""
			}
		}
		if ev.IsError {
			if text == "" {
				text = "豆包 CLI 返回错误"
			}
			return []Chunk{{Err: text}}
		}
		c := Chunk{Done: true, Final: text}
		return []Chunk{c}
	case "error":
		var text string
		_ = json.Unmarshal(ev.Result, &text)
		if text == "" {
			text = "豆包 CLI 返回错误"
		}
		return []Chunk{{Err: text}}
	}
	return nil
}

// chunksFromContent 解析 message.content。可能是字符串，也可能是 block 数组。
func chunksFromContent(raw json.RawMessage) []Chunk {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Chunk{{Delta: s}}
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		ToolUseID string          `json:"tool_use_id"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []Chunk
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, Chunk{Delta: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				out = append(out, Chunk{Reasoning: b.Thinking})
			}
		case "tool_use":
			// 入参要留全一点：界面靠它显示「改哪个文件、改了什么」，
			// 只留 160 字符会把路径截在中间，改动面板就认不出来。
			out = append(out, Chunk{
				Tool:      b.Name,
				ToolPhase: "start",
				CallID:    b.ID,
				Content:   summarizeInput(b.Input),
				Input:     clipText(rawString(b.Input), 2000),
				Path:      pathFromInput(b.Input),
			})
		case "tool_result":
			status := "ok"
			if b.IsError {
				status = "error"
			}
			out = append(out, Chunk{
				Tool:       "tool",
				ToolPhase:  "end",
				ToolStatus: status,
				CallID:     b.ToolUseID,
				Content:    clipText(textFromContent(b.Content), 4000),
			})
		}
	}
	return out
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// pathFromInput 从工具入参里取文件路径。不同 CLI 叫法不同，按优先级取第一个命中的。
func pathFromInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "notebook_path"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// textFromContent 把 tool_result 的 content（可能是字符串或 [{text:...}]）摊平成文本。
func textFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if b.Len() > 0 && p.Text != "" {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

func summarizeInput(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
