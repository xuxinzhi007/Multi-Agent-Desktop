// acp-probe 用一个真实 ACP Agent 跑一轮，把内部协议事件打到 stdout。
//
// 用途：接新 Agent（codex-acp / grok / dsh）时先在这里跑通，
// 再去改 desktop 的执行路径。它不依赖应用进程，排错面最小。
//
//	go run ./cmd/acp-probe -cmd grok -arg acp -prompt "看下这个仓库"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	rt "multi-agent-desktop/internal/runtime"
	"multi-agent-desktop/internal/runtime/acp"
	"multi-agent-desktop/internal/workdir"
)

type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, " ") }
func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	var args stringsFlag
	cmd := flag.String("cmd", "", "ACP Agent 可执行文件，例如 grok / codex-acp / dsh")
	cwd := flag.String("cwd", ".", "会话工作目录")
	prompt := flag.String("prompt", "列出当前目录的文件", "这一轮的用户输入")
	tracePath := flag.String("trace", "", "把收发原始帧写到这个文件（JSONL）")
	allow := flag.Bool("allow", false, "审批请求自动选择 allow_once；默认拒绝")
	timeout := flag.Duration("timeout", 10*time.Minute, "整轮超时")
	flag.Var(&args, "arg", "Agent 参数，可重复")
	flag.Parse()

	if *cmd == "" {
		fmt.Fprintln(os.Stderr, "缺少 -cmd")
		os.Exit(2)
	}
	abs, err := filepathAbs(*cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cwd:", err)
		os.Exit(1)
	}
	roots, err := workdir.NewRoots(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cwd:", err)
		os.Exit(1)
	}

	var trace *os.File
	if *tracePath != "" {
		trace, err = os.Create(*tracePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "trace:", err)
			os.Exit(1)
		}
		defer trace.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	r := &acp.Runtime{
		Command: *cmd,
		Args:    args,
		Options: acp.Options{
			Roots: roots,
			Trace: trace,
		},
	}
	started := time.Now()
	s, err := r.Start(ctx, rt.Spec{ID: "probe", WorkDir: abs})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	defer s.Close()

	if err := s.Prompt(ctx, rt.Prompt{Text: *prompt}); err != nil {
		fmt.Fprintln(os.Stderr, "prompt:", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	for ev := range s.Events() {
		_ = enc.Encode(ev)
		switch ev.Kind {
		case rt.KindPermission:
			choice := choose(ev.Permission, *allow)
			if err := s.RespondPermission(ev.Permission.ID, choice); err != nil {
				fmt.Fprintln(os.Stderr, "permission:", err)
			}
		case rt.KindTurnFinished:
			fmt.Fprintf(os.Stderr, "\n[done] reason=%s elapsed=%s\n", ev.Stop.Reason, time.Since(started).Round(time.Millisecond))
			return
		}
	}
}

func choose(p *rt.Permission, allow bool) string {
	want := rt.PermissionRejectOnce
	if allow {
		want = rt.PermissionAllowOnce
	}
	for _, o := range p.Options {
		if o.Kind == want {
			return o.ID
		}
	}
	if len(p.Options) > 0 {
		return p.Options[0].ID
	}
	return "reject"
}

func filepathAbs(p string) (string, error) {
	if p == "" {
		p = "."
	}
	return filepath.Abs(p)
}
