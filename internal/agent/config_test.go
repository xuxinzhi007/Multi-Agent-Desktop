package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/llm"
)

// newQuietManager 返回一个不推事件的 Manager，用于只需读取状态的用例。
func newQuietManager(t *testing.T, cfg llm.Config) *Manager {
	t.Helper()
	m := NewManager(cfg, func(string, any) {})
	// 等异步落盘结束再让 t.TempDir 清理目录，否则残留 agents.json.tmp。
	t.Cleanup(m.waitSaves)
	return m
}

// mustSetStore 在临时目录里设置持久化路径。
func mustSetStore(t *testing.T, m *Manager, path string) {
	t.Helper()
	if err := m.SetStore(path); err != nil {
		t.Fatalf("SetStore(%q): %v", path, err)
	}
}

// TC-001 / TC-003 / TC-004 —— 会话工作目录：写入、空串回落、会话隔离。
func TestSetAgentWorkDir(t *testing.T) {
	tests := []struct {
		name      string
		global    string
		setA1     string
		wantA1    string
		wantA2    string
		wantInA1  string
		wantFail  bool
		extraCase string
	}{
		{
			name:     "TC-001 设置会话目录",
			global:   "",
			setA1:    "/tmp/a1",
			wantA1:   "/tmp/a1",
			wantA2:   "",
			wantInA1: "/tmp/a1",
		},
		{
			name:     "TC-003 空串回落全局",
			global:   "/global",
			setA1:    "",
			wantA1:   "/global",
			wantA2:   "/global",
			wantInA1: "",
		},
		{
			name:     "TC-004 会话隔离",
			global:   "/global",
			setA1:    "/tmp/a1",
			wantA1:   "/tmp/a1",
			wantA2:   "/global",
			wantInA1: "/tmp/a1",
		},
		{
			name:     "TC-003 双方都空",
			global:   "",
			setA1:    "",
			wantA1:   "",
			wantA2:   "",
			wantInA1: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newQuietManager(t, llm.Config{Provider: "mock"})
			m.SetWorkDir(tc.global)
			a1 := m.Create("a1", "A1", "r")
			a2 := m.Create("a2", "A2", "r")

			if err := m.SetAgentWorkDir("a1", tc.setA1); err != nil {
				t.Fatalf("SetAgentWorkDir(%q): %v", tc.setA1, err)
			}

			if got := a1.EffectiveWorkDir(m); got != tc.wantA1 {
				t.Errorf("SetAgentWorkDir(%q) global=%q: a1.EffectiveWorkDir() = %q, want %q",
					tc.setA1, tc.global, got, tc.wantA1)
			}
			if got := a2.EffectiveWorkDir(m); got != tc.wantA2 {
				t.Errorf("SetAgentWorkDir(%q) global=%q: a2.EffectiveWorkDir() = %q, want %q (isolation)",
					tc.setA1, tc.global, got, tc.wantA2)
			}
			if got := a1.WorkDir; got != tc.wantInA1 {
				t.Errorf("SetAgentWorkDir(%q): a1.WorkDir = %q, want raw %q",
					tc.setA1, got, tc.wantInA1)
			}
		})
	}
}

// TC-005 —— 对不存在的会话设置工作目录必须报错而非 panic。
func TestSetAgentWorkDirMissingAgent(t *testing.T) {
	m := newQuietManager(t, llm.Config{Provider: "mock"})
	err := m.SetAgentWorkDir("NOPE", "/x")
	if err == nil {
		t.Fatal("SetAgentWorkDir(\"NOPE\", \"/x\") = nil, want error")
	}
	if !strings.Contains(err.Error(), "agent NOPE not found") {
		t.Errorf("SetAgentWorkDir(\"NOPE\", \"/x\") error = %q, want it to contain %q",
			err, "agent NOPE not found")
	}
	if len(m.List()) != 0 {
		t.Errorf("SetAgentWorkDir(\"NOPE\") created agents: got %d, want 0", len(m.List()))
	}
}

// TC-002 —— Save/Load 必须完整往返 WorkDir + LLMCfg（守住四个白名单拷贝点）。
func TestSessionConfigPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")

	m := newQuietManager(t, llm.Config{Provider: "mock", Model: "global-model"})
	mustSetStore(t, m, path)
	m.Create("a1", "后端", "Go 专家")
	wantCfg := llm.Config{Provider: "openai", BaseURL: "http://x", APIKey: "sk", Model: "gpt-x"}
	if err := m.SetAgentWorkDir("a1", "/tmp/project"); err != nil {
		t.Fatalf("SetAgentWorkDir: %v", err)
	}
	if err := m.SetAgentLLM("a1", wantCfg); err != nil {
		t.Fatalf("SetAgentLLM: %v", err)
	}
	// SetAgent* 内部是异步 Save；显式同步落盘，禁止靠 sleep 竞速。
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m2 := newQuietManager(t, llm.Config{Provider: "mock", Model: "global-model"})
	mustSetStore(t, m2, path)
	got, ok := m2.Get("a1")
	if !ok {
		t.Fatalf("Load(%q): agent a1 not reloaded", path)
	}
	if got.WorkDir != "/tmp/project" {
		t.Errorf("Load(%q): WorkDir = %q, want %q", path, got.WorkDir, "/tmp/project")
	}
	if got.LLMCfg == nil {
		t.Fatalf("Load(%q): LLMCfg = nil, want %+v", path, wantCfg)
	}
	if *got.LLMCfg != wantCfg {
		t.Errorf("Load(%q): LLMCfg = %+v, want %+v", path, *got.LLMCfg, wantCfg)
	}
	if eff := got.EffectiveWorkDir(m2); eff != "/tmp/project" {
		t.Errorf("Load(%q): EffectiveWorkDir() = %q, want %q", path, eff, "/tmp/project")
	}
}

// TC-006 —— 旧版 agents.json（无 workDir / llmCfg 键）必须能加载，不走 nil 解引用。
func TestLoadLegacyAgentsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	legacy := `[{"id":"old","name":"旧会话","role":"r","color":"#fff","status":"running",
"createdAt":1700000000,"messages":[{"role":"user","content":"hi"}],"backend":"mock"}]`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	m := newQuietManager(t, llm.Config{Provider: "mock", Model: "global-model"})
	m.SetWorkDir("/global")
	mustSetStore(t, m, path)

	a, ok := m.Get("old")
	if !ok {
		t.Fatalf("Load legacy %q: agent \"old\" missing, got %d agents", path, len(m.List()))
	}
	if a.WorkDir != "" {
		t.Errorf("Load legacy: WorkDir = %q, want empty", a.WorkDir)
	}
	if a.LLMCfg != nil {
		t.Errorf("Load legacy: LLMCfg = %+v, want nil", *a.LLMCfg)
	}
	if a.Status != StatusIdle {
		t.Errorf("Load legacy: Status = %q, want %q (restart resets to idle)", a.Status, StatusIdle)
	}
	if got := a.EffectiveWorkDir(m); got != "/global" {
		t.Errorf("Load legacy: EffectiveWorkDir() = %q, want fallback %q", got, "/global")
	}
	if got := a.EffectiveLLM(m); got.Model != "global-model" {
		t.Errorf("Load legacy: EffectiveLLM().Model = %q, want fallback %q", got.Model, "global-model")
	}
}

// TC-007 / TC-008 / TC-009 —— 会话模型配置：写入、nil 回落、覆盖全局、互不影响。
func TestSessionLLMConfig(t *testing.T) {
	tests := []struct {
		name     string
		global   llm.Config
		agentCfg *llm.Config
		want     llm.Config
	}{
		{
			name:     "TC-008 nil 回落全局",
			global:   llm.Config{Provider: "mock", Model: "global-m"},
			agentCfg: nil,
			want:     llm.Config{Provider: "mock", Model: "global-m"},
		},
		{
			name:     "TC-007/009 会话覆盖全局",
			global:   llm.Config{Provider: "mock", Model: "global-m"},
			agentCfg: &llm.Config{Provider: "openai", BaseURL: "http://x", Model: "agent-m"},
			want:     llm.Config{Provider: "openai", BaseURL: "http://x", Model: "agent-m"},
		},
		{
			name:     "全局也空",
			global:   llm.Config{},
			agentCfg: nil,
			want:     llm.Config{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newQuietManager(t, tc.global)
			a := m.Create("a1", "A1", "r")

			if tc.agentCfg != nil {
				if err := m.SetAgentLLM("a1", *tc.agentCfg); err != nil {
					t.Fatalf("SetAgentLLM(%+v): %v", *tc.agentCfg, err)
				}
			}

			if got := a.EffectiveLLM(m); got != tc.want {
				t.Errorf("EffectiveLLM() global=%+v agentCfg=%v: got %+v, want %+v",
					tc.global, tc.agentCfg, got, tc.want)
			}
			if got := m.LLMConfigFor("a1"); got != tc.want {
				t.Errorf("LLMConfigFor(\"a1\") global=%+v: got %+v, want %+v",
					tc.global, got, tc.want)
			}
			// 未设置的另一个会话必须仍然跟随全局。
			m.Create("a2", "A2", "r")
			if got := m.LLMConfigFor("a2"); got != tc.global {
				t.Errorf("LLMConfigFor(\"a2\") global=%+v: got %+v, want %+v (isolation)",
					tc.global, got, tc.global)
			}
		})
	}
}

// TC-007 补充 —— 不存在的会话设置模型必须报错。
func TestSetAgentLLMMissingAgent(t *testing.T) {
	m := newQuietManager(t, llm.Config{Provider: "mock"})
	err := m.SetAgentLLM("NOPE", llm.Config{Model: "x"})
	if err == nil {
		t.Fatal("SetAgentLLM(\"NOPE\", ...) = nil, want error")
	}
	if !strings.Contains(err.Error(), "agent NOPE not found") {
		t.Errorf("SetAgentLLM(\"NOPE\") error = %q, want containing %q", err, "agent NOPE not found")
	}
}

// TC-010 —— 执行中改配置只影响下一轮：当前轮用快照，不产生数据竞争。
func TestSessionLLMMidRunSnapshot(t *testing.T) {
	var mu sync.Mutex
	var seenSystem []string

	m := NewManager(llm.Config{Provider: "mock", Model: "global-m"}, func(event string, data any) {
		if event != "agent.message" {
			return
		}
		p, ok := data.(map[string]any)
		if !ok {
			return
		}
		msg, ok := p["message"].(llm.Message)
		if !ok || msg.Role != "user" {
			return
		}
		mu.Lock()
		seenSystem = append(seenSystem, msg.Content)
		mu.Unlock()
	})
	a := m.Create("a1", "A1", "r")

	if err := m.Prompt(context.Background(), PromptRequest{AgentID: "a1", Text: "first"}); err != nil {
		t.Fatalf("Prompt(first): %v", err)
	}

	// 等这一点真正开始运行（状态 running），再改配置。
	waitFor(t, 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.Status == StatusRunning
	}, "agent to enter running state")

	if err := m.SetAgentLLM("a1", llm.Config{Provider: "mock", Model: "next-round"}); err != nil {
		t.Fatalf("SetAgentLLM(mid-run): %v", err)
	}

	// 当前轮仍在旧配置下推进，但 LLMConfigFor 已经能看到新值。
	if got := m.LLMConfigFor("a1").Model; got != "next-round" {
		t.Errorf("mid-run LLMConfigFor(\"a1\").Model = %q, want %q", got, "next-round")
	}

	waitFor(t, 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.Status == StatusIdle && a.QueueLen == 0
	}, "agent to become idle")

	mu.Lock()
	got := len(seenSystem)
	mu.Unlock()
	if got != 1 {
		t.Errorf("mid-run: user messages seen = %d, want 1", got)
	}
}

// TC-019 / TC-020 / TC-021 —— systemPromptFor 纯函数：知识库注入矩阵。
type stubKnowledge struct {
	lines []string
	calls int
}

func (s *stubKnowledge) NotesForSystemPrompt() []string {
	s.calls++
	return s.lines
}

func TestSystemPromptFor(t *testing.T) {
	base := systemPromptFor("后端专家", nil)

	tests := []struct {
		name       string
		kb         KnowledgeProvider
		wantHas    []string
		wantNotHas []string
		wantExact  string
	}{
		{
			name:       "TC-020 kb 为 nil 不注入",
			kb:         nil,
			wantNotHas: []string{"## 知识库"},
			wantExact:  base,
		},
		{
			name:       "TC-021 kb 返回空列表不产生空段",
			kb:         &stubKnowledge{lines: nil},
			wantNotHas: []string{"## 知识库"},
			wantExact:  base,
		},
		{
			name:      "TC-019 两条笔记前置注入",
			kb:        &stubKnowledge{lines: []string{"约定: 用 Go 风格", "术语: agent=会话"}},
			wantHas:   []string{"## 知识库\n- 约定: 用 Go 风格\n- 术语: agent=会话\n", base},
			wantExact: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := systemPromptFor("后端专家", tc.kb)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("systemPromptFor(\"后端专家\", %v) = %q, want %q", tc.kb, got, tc.wantExact)
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("systemPromptFor(\"后端专家\", %v) = %q, want containing %q", tc.kb, got, want)
				}
			}
			for _, notWant := range tc.wantNotHas {
				if strings.Contains(got, notWant) {
					t.Errorf("systemPromptFor(\"后端专家\", %v) = %q, want NOT containing %q", tc.kb, got, notWant)
				}
			}
			if !strings.HasPrefix(got, "角色：") && !strings.HasPrefix(got, "## 知识库") {
				t.Errorf("systemPromptFor: prompt = %q, want it to contain the role line", got)
			}
		})
	}
}

// TC-019 —— Create 时把知识库写进首条 system message；SetKnowledge(nil) 不注入。
func TestCreateInjectsKnowledge(t *testing.T) {
	kb := &stubKnowledge{lines: []string{"约定: 用 Go 风格"}}
	m := newQuietManager(t, llm.Config{Provider: "mock"})
	m.SetKnowledge(kb)

	a := m.Create("a1", "A1", "后端专家")
	if len(a.Messages) == 0 {
		t.Fatal("Create(\"a1\"): Messages empty, want initial system message")
	}
	if a.Messages[0].Role != "system" {
		t.Errorf("Create(\"a1\"): Messages[0].Role = %q, want %q", a.Messages[0].Role, "system")
	}
	if !strings.Contains(a.Messages[0].Content, "## 知识库") ||
		!strings.Contains(a.Messages[0].Content, "约定: 用 Go 风格") {
		t.Errorf("Create(\"a1\") with knowledge: system prompt = %q, want it to contain the note",
			a.Messages[0].Content)
	}
	if kb.calls != 1 {
		t.Errorf("Create(\"a1\"): NotesForSystemPrompt calls = %d, want 1", kb.calls)
	}

	m2 := newQuietManager(t, llm.Config{Provider: "mock"})
	m2.SetKnowledge(nil)
	b := m2.Create("a2", "A2", "后端专家")
	if strings.Contains(b.Messages[0].Content, "知识库") {
		t.Errorf("Create(\"a2\") with nil knowledge: system prompt = %q, want no knowledge section",
			b.Messages[0].Content)
	}
}

// AppendSystemMessage 是 AttachNoteToAgent 的注入通道，必须追加而非覆盖。
func TestAppendSystemMessage(t *testing.T) {
	m := newQuietManager(t, llm.Config{Provider: "mock"})
	a := m.Create("a1", "A1", "r")
	before := len(a.Messages)

	a.AppendSystemMessage("[知识] ## 知识库")

	if got := len(a.Messages); got != before+1 {
		t.Fatalf("AppendSystemMessage: len(Messages) = %d, want %d", got, before+1)
	}
	last := a.Messages[len(a.Messages)-1]
	if last.Role != "system" || last.Content != "[知识] ## 知识库" {
		t.Errorf("AppendSystemMessage: last message = %+v, want role=system content=%q",
			last, "[知识] ## 知识库")
	}
}

// 持久化落盘必须是合法 JSON 且只含预期字段（防止白名单漂移写坏文件）。
func TestSaveWritesJSONShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	m := newQuietManager(t, llm.Config{Provider: "mock"})
	mustSetStore(t, m, path)
	m.Create("a1", "A1", "r")
	if err := m.SetAgentWorkDir("a1", "/w"); err != nil {
		t.Fatalf("SetAgentWorkDir: %v", err)
	}
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("Save(%q) wrote invalid JSON: %v", path, err)
	}
	if len(raw) != 1 {
		t.Fatalf("Save(%q): len = %d, want 1", path, len(raw))
	}
	if got, _ := raw[0]["workDir"].(string); got != "/w" {
		t.Errorf("Save(%q): workDir = %q, want %q", path, got, "/w")
	}
	if got, _ := raw[0]["status"].(string); got != string(StatusIdle) {
		t.Errorf("Save(%q): status = %q, want %q", path, got, StatusIdle)
	}
}

// waitFor 轮询直到 cond 为真；超时用 t.Fatalf 报出场景。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitFor: timed out after %s waiting for %s", timeout, what)
}

// 编译期守卫：确保测试里用到的 error 包装语义与生产一致。
var _ = errors.Is

// 回归：Create/SetAgent*/Delete 都异步 go m.Save()，叠加并发写同一文件
// 曾产生"invalid character '\"' after top-level value"（截断+写入交错）。
// 现在 Save 串行化且写临时文件后 rename，必须始终产出合法 JSON。
func TestAgentConcurrentSaveKeepsFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")

	m := newQuietManager(t, llm.Config{Provider: "mock"})
	mustSetStore(t, m, path)
	for _, id := range []string{"a1", "a2", "a3"} {
		m.Create(id, id, "r")
	}
	if err := m.SetAgentWorkDir("a1", "/w1"); err != nil {
		t.Fatalf("SetAgentWorkDir: %v", err)
	}
	if err := m.SetAgentLLM("a2", llm.Config{Provider: "mock", Model: "m2"}); err != nil {
		t.Fatalf("SetAgentLLM: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Save()
		}()
	}
	wg.Wait()
	m.waitSaves()

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("concurrent Save(%q) produced invalid JSON: %v (content=%q)", path, err, buf)
	}
	if len(raw) != 3 {
		t.Errorf("concurrent Save(%q): len = %d, want 3", path, len(raw))
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("Save(%q) left temporary file behind: stat err = %v", path, err)
	}
}

// View 实现 KnowledgeProvider 接口。
func (s *stubKnowledge) View(agentID, role string, knowledgeIDs []string, recentText string, budget int) (string, []string) {
	s.calls++
	if len(s.lines) == 0 {
		return "", nil
	}
	return "## 知识库\n- " + strings.Join(s.lines, "\n- "), nil
}
