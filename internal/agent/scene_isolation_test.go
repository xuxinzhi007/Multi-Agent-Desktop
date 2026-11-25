package agent

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

	"multi-agent-desktop/internal/llm"
)

// captureModel 起一个假 OpenAI 端点，把每次请求里的 messages 收下来。
// 返回取消函数和读取器。用它验证「模型实际看到了什么」，而不是只看会话记录。
func captureModel(t *testing.T) (baseURL string, seen func() [][]llm.Message) {
	t.Helper()
	var mu sync.Mutex
	var turns [][]llm.Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		turns = append(turns, body.Messages)
		mu.Unlock()
		w.Header().Set("content-type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte("data: " + s + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		write(`{"choices":[{"delta":{"content":"收到"}}]}`)
		write(`[DONE]`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() [][]llm.Message {
		mu.Lock()
		defer mu.Unlock()
		return append([][]llm.Message(nil), turns...)
	}
}

func containsMsg(turns [][]llm.Message, needle string) bool {
	for _, msgs := range turns {
		for _, m := range msgs {
			if strings.Contains(m.Content, needle) {
				return true
			}
		}
	}
	return false
}

// 群回合的 prompt 不能带私聊历史。
//
// 以前 history 以 a.Messages 起头，私聊里的旧话题无条件混进每个群回合，
// 模型拿着远古历史回答当前群的问题（用户原话：回复牛头不对马嘴）。
func TestChannelTurnPromptExcludesPrivateHistory(t *testing.T) {
	baseURL, seen := captureModel(t)
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	mustSetStore(t, m, filepath.Join(t.TempDir(), "agents.json"))

	a := m.Create("a1", "甲", "后端")
	if err := m.SetAgentWorkDir(a.ID, workDirForTest(t)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentLLM(a.ID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	const privateSecret = "私聊旧话题_zzz_只该出现在私聊"
	// 先跑一轮私聊，让 a.Messages 里真的有历史。
	if _, err := m.RunBlocks(context.Background(), a.ID, privateSecret, nil); err != nil {
		t.Fatalf("私聊回合: %v", err)
	}
	if !containsMsg(seen(), privateSecret) {
		t.Fatal("私聊回合本身没把消息送给模型，测试前提不成立")
	}

	before := len(seen())
	// 群回合。Context 是频道 transcript，由调用方给。
	if err := m.Prompt(context.Background(), PromptRequest{
		AgentID:   a.ID,
		Text:      "群里的问题",
		ChannelID: "ch-1",
		Context:   "## 群聊上下文\n你：之前群里说过的一句",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(seen()) > before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	turns := seen()
	if len(turns) == 0 {
		t.Fatal("没有任何模型请求")
	}
	last := turns[len(turns)-1]
	var joined strings.Builder
	for _, msg := range last {
		joined.WriteString(msg.Content)
		joined.WriteByte('\n')
	}
	if strings.Contains(joined.String(), privateSecret) {
		t.Fatalf("群回合 prompt 带了私聊历史：\n%s", joined.String())
	}
	if !strings.Contains(joined.String(), "群里的问题") {
		t.Fatal("群回合 prompt 里没有本条消息")
	}
	if !strings.Contains(joined.String(), "之前群里说过的一句") {
		t.Fatal("群回合 prompt 里没有频道 transcript")
	}
	// 等异步落盘结束，否则 t.TempDir 清理会撞上仍在写的文件。
	m.waitSaves()
	time.Sleep(100 * time.Millisecond)
}

// 检查点键必须区分场景：群回合不能把带群上文的检查点写到私聊的键上。
func TestCheckpointKeySeparatesScenes(t *testing.T) {
	baseURL, _ := captureModel(t)
	dir := t.TempDir()
	m := NewManager(llm.Config{}, func(string, any) {})
	t.Cleanup(m.waitSaves)
	mustSetStore(t, m, filepath.Join(dir, "agents.json"))

	a := m.Create("a1", "甲", "后端")
	if err := m.SetAgentWorkDir(a.ID, workDirForTest(t)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgentLLM(a.ID, llm.Config{
		Provider: "openai", BaseURL: baseURL, APIKey: "k", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Prompt(context.Background(), PromptRequest{
		AgentID: a.ID, Text: "群里的话", ChannelID: "ch-9", Context: "## 群聊上下文\n你：群内容",
	}); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dir, "loop", "runs")
	deadline := time.Now().Add(5 * time.Second)
	var groupKey string
	for time.Now().Before(deadline) {
		groupKey = filepath.Join(runs, "a1__ch-9.json")
		if fileExists(groupKey) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fileExists(groupKey) {
		t.Fatalf("群回合没有按 agent+scene 落检查点：%s", groupKey)
	}
	// 私聊的键不该被群回合创建或污染。
	if fileExists(filepath.Join(runs, "a1.json")) {
		raw := readFileForTest(t, filepath.Join(runs, "a1.json"))
		if strings.Contains(raw, "群内容") || strings.Contains(raw, "群里的话") {
			t.Fatalf("群回合把群上文写进了私聊检查点：\n%s", raw)
		}
	}
	m.waitSaves()
	time.Sleep(100 * time.Millisecond)
}
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readFileForTest(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// workDirForTest 给一个不受 t.TempDir 自动清理影响的目录。
// 后台 goroutine（摘要、图片落盘）可能在测试结束后仍写这里，
// 用 t.TempDir 会让 cleanup 撞上 "directory not empty"。
func workDirForTest(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "mad-scene-work-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
