// Package audit 记录每一次有副作用的工具调用。
//
// 为什么单独一份文件，而不是只存在会话历史里：
// 会话会被删、会被摘要压缩、会被前端截断到一页；审计要能独立回答
// 「谁在什么时候跑了什么命令、成没成功」。JSONL 追加写，坏了也只坏一行。
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry 是一条审计记录。
type Entry struct {
	At         int64  `json:"at"`
	AgentID    string `json:"agentId"`
	AgentName  string `json:"agentName"`
	Tool       string `json:"tool"`
	CallID     string `json:"callId,omitempty"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	WorkDir    string `json:"workDir,omitempty"`
}

// Log 是追加写的审计文件。零值不可用，用 Open。
type Log struct {
	mu   sync.Mutex
	path string
}

// Open 打开（或准备）一个审计文件。path 为空时所有写入静默丢弃。
func Open(path string) *Log {
	return &Log{path: path}
}

// Path 返回文件位置，界面可以告诉用户日志在哪。
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Append 追加一条记录。
func (l *Log) Append(e Entry) error {
	if l == nil || l.path == "" {
		return nil
	}
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Recent 返回最近 n 条。文件缺失时返回空，不报错。
func (l *Log) Recent(n int) ([]Entry, error) {
	if l == nil || l.path == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 50
	}
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 逐行扫、只留最后 n 条。审计文件按天也不会大到需要专门索引。
	out := make([]Entry, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue // 坏行跳过，不让一条脏数据挡住整个列表
		}
		out = append(out, e)
		if len(out) > n {
			out = out[1:]
		}
	}
	return out, sc.Err()
}
