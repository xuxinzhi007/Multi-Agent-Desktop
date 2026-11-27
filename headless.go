package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/mattn/go-isatty"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	gwhost "multi-agent-desktop/internal/gateway/host"
)

var cliIO atomic.Pointer[headlessIO]

var newDesktopWindow = openDesktopWindow

func continueDesktop(headless bool, api *API) *mygo.Window {
	if headless {
		return nil
	}
	return newDesktopWindow(api)
}

func headlessFlags(args []string) (bool, string) {
	on := false
	id := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-headless":
			on = true
		case "-agent":
			if i+1 < len(args) {
				i++
				id = args[i]
			}
		}
	}
	return on, id
}

type headlessIO struct {
	out    io.Writer
	in     *bufio.Reader
	center *approval.Center
	send   func(event string, data any)
	mu     sync.Mutex
}

func (h *headlessIO) write(event string, data any) {
	if h == nil {
		return
	}
	if h.send != nil {
		h.send(event, data)
		return
	}
	switch event {
	case "agent.delta":
		if v, ok := toAgentDelta(data); ok && v.Delta != "" {
			h.printf("%s", v.Delta)
		}
	case "agent.message":
		if v, ok := toAgentMessage(data); ok && v.Message.Role == "assistant" && v.Message.Content != "" {
			h.printf("\n%s\n", v.Message.Content)
		}
	case "agent.error":
		if v, ok := toAgentError(data); ok && v.Error != "" {
			h.printf("\n错误：%s\n", v.Error)
		}
	case "tool.approval":
		h.decide(data)
	}
}

func (h *headlessIO) decide(data any) {
	v, ok := toApprovalEvent(data)
	if !ok || h.center == nil {
		return
	}
	h.mu.Lock()
	fmt.Fprintf(h.out, "\n审批 %s %s\n%s\n[y/n/always] ", v.ID, v.Tool, v.Title)
	line, err := h.readLineLocked()
	h.mu.Unlock()
	if err != nil {
		_ = h.center.Decide(v.ID, false, false, "终端读取失败")
		return
	}
	allow, remember := headlessDecision(line)
	reason := ""
	if !allow {
		reason = "终端拒绝"
	}
	_ = h.center.Decide(v.ID, allow, remember, reason)
}

func headlessDecision(line string) (allow, remember bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, false
	case "always", "a":
		return true, true
	default:
		return false, false
	}
}

func (h *headlessIO) printf(format string, args ...any) {
	if h == nil || h.out == nil {
		return
	}
	h.mu.Lock()
	fmt.Fprintf(h.out, format, args...)
	h.mu.Unlock()
}

func (h *headlessIO) readLine() (string, error) {
	if h == nil {
		return "", io.EOF
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readLineLocked()
}

func (h *headlessIO) readLineLocked() (string, error) {
	if h.in == nil {
		return "", io.EOF
	}
	s, err := h.in.ReadString('\n')
	if len(s) == 0 && err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func pickHeadlessAgent(mgr *agent.Manager, id string) (string, error) {
	if mgr == nil {
		return "", errors.New("没有会话内核")
	}
	if id != "" {
		if _, ok := mgr.Get(id); !ok {
			return "", fmt.Errorf("没有这个会话：%s", id)
		}
		return id, nil
	}
	list := mgr.List()
	if len(list) == 0 {
		return "", errors.New("还没有会话")
	}
	return list[0].ID, nil
}

func headlessTerminal(h *headlessIO) bool {
	if h == nil || h.out == nil {
		return false
	}
	f, ok := h.out.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func runHeadless(ctx context.Context, h *headlessIO, mgr *agent.Manager, agentID string) error {
	if headlessTerminal(h) {
		return runTermUI(ctx, h, mgr, agentID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := pickHeadlessAgent(mgr, agentID)
	if err != nil {
		return err
	}
	out := io.Writer(io.Discard)
	if h != nil && h.out != nil {
		out = h.out
	}
	fmt.Fprintf(out, "会话 %s\n", id)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(out, "> ")
		line, err := h.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := mgr.RunPrompt(ctx, id, line); err != nil {
			fmt.Fprintf(out, "错误：%s\n", err.Error())
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

var (
	shutdownTrace   *[]string
	shutdownTraceMu sync.Mutex
)

func noteShutdown(step string) {
	shutdownTraceMu.Lock()
	defer shutdownTraceMu.Unlock()
	if shutdownTrace == nil {
		return
	}
	*shutdownTrace = append(*shutdownTrace, step)
}

func shutdownRuntime(api *API, mgr *agent.Manager, gw *gwhost.Host) {
	if api != nil && api.bot != nil {
		api.bot.Sync(nil)
	}
	if planSvc != nil {
		planSvc.Stop()
	}
	if memBridge != nil {
		memBridge.Close()
	}
	if memStore != nil {
		_ = memStore.Close()
	}
	noteShutdown("gateway")
	if gw != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = gw.Close(closeCtx)
		cancel()
	}
	noteShutdown("manager")
	if mgr != nil {
		if mcps := mgr.MCP(); mcps != nil {
			mcps.Close()
		}
		mgr.Shutdown()
	}
}
