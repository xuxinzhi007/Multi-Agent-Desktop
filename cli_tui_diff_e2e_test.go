package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/llm"
)

// editingModelServer 起一个假 OpenAI 端点：第一轮让模型调用一次 edit_file，
// 拿到工具结果后第二轮只回一句话。用来把「真工具 → 真事件 → 终端界面」整条链跑一遍。
func editingModelServer(t *testing.T, editArgs string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawTool := false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				sawTool = true
			}
		}
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		if sawTool {
			write(`{"choices":[{"delta":{"content":"改好了"}}]}`)
			write(`[DONE]`)
			return
		}
		args, _ := json.Marshal(editArgs)
		write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_edit_1","function":{"name":"edit_file","arguments":""}}]}}]}`)
		write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(args) + `}}]}}]}`)
		write(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		write(`[DONE]`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// delegatingModelServer 起一个假 OpenAI 端点，同时扮演主会话和它派出去的子任务：
// 主会话第一轮调用 delegate，子任务第一轮调用 edit_file，各自拿到工具结果后回一句话。
// 靠系统提示分辨是谁在问——子任务的系统提示里有「临时子任务」。
func delegatingModelServer(t *testing.T, delegateArgs, editArgs string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawTool, isChild := false, false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				sawTool = true
			}
			if m.Role == "system" {
				var s string
				if json.Unmarshal(m.Content, &s) != nil {
					s = string(m.Content)
				}
				if strings.Contains(s, "临时子任务") {
					isChild = true
				}
			}
		}
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		call := func(name, rawArgs string) {
			args, _ := json.Marshal(rawArgs)
			write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"` + name + `","arguments":""}}]}}]}`)
			write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(args) + `}}]}}]}`)
			write(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			write(`[DONE]`)
		}
		switch {
		case isChild && !sawTool:
			call("edit_file", editArgs)
		case isChild:
			write(`{"choices":[{"delta":{"content":"notes.txt 已经改好"}}]}`)
			write(`[DONE]`)
		case !sawTool:
			call("delegate", delegateArgs)
		default:
			write(`{"choices":[{"delta":{"content":"子任务改完了"}}]}`)
			write(`[DONE]`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 派活整条链的回归：主会话调用 delegate(mode=write) → 临时子任务在主会话目录里真的改了文件
// → 主对话里能看到这次改动的 diff，并标出是哪个子任务改的。
// 同时确认「主流形态」的两条底线：不多出会话，不开工作树。
func TestCLIShowsDiffForDelegatedEdit(t *testing.T) {
	dir := testWorkDir(t)
	target := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(target, []byte("第一行\n旧的说明\n第三行\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		events []cliMsg
	)
	mgr := agent.NewManager(llm.Config{}, func(event string, data any) {
		mu.Lock()
		events = append(events, cliMsg{event: event, data: data})
		mu.Unlock()
	})
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 100, 44
	m.layout()
	sessionsBefore := len(mgr.List())

	baseURL := delegatingModelServer(t,
		`{"mode":"write","tasks":[{"name":"改说明","task":"把 notes.txt 里的旧的说明改成新的说明"}]}`,
		`{"path":"notes.txt","oldText":"旧的说明","newText":"新的说明"}`,
	)
	if err := mgr.SetAgentLLM(m.agentID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := mgr.RunPrompt(ctx, m.agentID, "找个子任务把 notes.txt 里的旧的说明改成新的说明"); err != nil {
		t.Fatalf("这一轮没跑完：%v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "新的说明") {
		t.Fatalf("子任务没有改到主会话目录里的文件：%q", got)
	}
	// 一个都不能多：既没有派活建出来的子会话，也没有旧版本那种常驻的「X的学习」。
	// 一轮结束后的收尾在后台跑，等一小会儿再数，免得数在它前面。
	time.Sleep(300 * time.Millisecond)
	var names []string
	for _, a := range mgr.List() {
		names = append(names, a.Snapshot().Name)
	}
	if len(names) != sessionsBefore {
		t.Fatalf("这一轮不该多出会话：之前 %d 个，现在是 %v", sessionsBefore, names)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mad-worktrees")); err == nil {
		t.Fatalf("临时子任务不该开工作树")
	}

	mu.Lock()
	replay := append([]cliMsg(nil), events...)
	mu.Unlock()
	for _, ev := range replay {
		m.onEvent(ev)
	}
	m.layout()
	view := m.View()
	for _, want := range []string{"子智能体协作", "就地修改", "子任务 改说明", "旧的说明", "新的说明", "变更 +1 -1 行"} {
		if !strings.Contains(view, want) {
			t.Fatalf("主对话里缺少 %q：\n%s", want, view)
		}
	}
}

// 整条链的回归：模型真的调用 edit_file 改了磁盘上的文件之后，
// 终端界面的对话流里要能看到带行号的 +/- 改动，Ctrl+O 的记录里也要有完整 diff。
//
// 已有的界面测试喂的是手写事件，证明不了「真实工具产出的 diff 走得到界面」。
func TestCLIShowsDiffForRealEditFileCall(t *testing.T) {
	dir := testWorkDir(t)
	target := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(target, []byte("第一行\n旧的说明\n第三行\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		events []cliMsg
	)
	mgr := agent.NewManager(llm.Config{}, func(event string, data any) {
		mu.Lock()
		events = append(events, cliMsg{event: event, data: data})
		mu.Unlock()
	})
	t.Cleanup(mgr.Shutdown)

	m := newCLIModel(context.Background(), mgr, "", dir, false)
	m.width, m.height = 100, 40
	m.layout()

	baseURL := editingModelServer(t, `{"path":"notes.txt","oldText":"旧的说明","newText":"新的说明"}`)
	if err := mgr.SetAgentLLM(m.agentID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := mgr.RunPrompt(ctx, m.agentID, "把 notes.txt 里的旧的说明改成新的说明"); err != nil {
		t.Fatalf("这一轮没跑完：%v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "新的说明") {
		t.Fatalf("文件没有被改，链路前半段就断了：%q", got)
	}

	mu.Lock()
	replay := append([]cliMsg(nil), events...)
	mu.Unlock()

	var endDiff string
	sawEnd := false
	for _, ev := range replay {
		if ev.event != "agent.tool" {
			continue
		}
		mp, _ := ev.data.(map[string]any)
		if mp["tool"] == "edit_file" && mp["phase"] == "end" {
			sawEnd = true
			endDiff, _ = mp["diff"].(string)
		}
	}
	if !sawEnd {
		t.Fatalf("没有收到 edit_file 的结束事件")
	}
	if !strings.Contains(endDiff, "-旧的说明") || !strings.Contains(endDiff, "+新的说明") {
		t.Fatalf("结束事件里没有真实 diff：%q", endDiff)
	}

	for _, ev := range replay {
		m.onEvent(ev)
	}
	m.layout()
	view := m.View()
	for _, want := range []string{"旧的说明", "新的说明", "变更 +1 -1 行"} {
		if !strings.Contains(view, want) {
			t.Fatalf("对话流里缺少 %q：\n%s", want, view)
		}
	}

	found := false
	for _, r := range m.tools {
		if r.Tool == "edit_file" && strings.Contains(r.Diff, "+新的说明") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Ctrl+O 的工具记录里没有 diff：%+v", m.tools)
	}
}
