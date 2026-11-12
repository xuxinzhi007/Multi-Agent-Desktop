package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/channel"
	"multi-agent-desktop/internal/llm"
)

// newTestSidecar 构造一个不连真实窗口的 Sidecar。
// 事件发到零值 Window：mygo 只在 Encode/入队时用到它，
// 零值未销毁、无 native 句柄，push 是安全 no-op（真正的 nil 会 panic）。
func newTestSidecar(t *testing.T) *Sidecar {
	t.Helper()
	mgr := agent.NewManager(llm.Config{Provider: "mock", Model: "global-m"}, func(string, any) {})
	t.Cleanup(func() {
		for _, a := range mgr.List() {
			mgr.Stop(a.ID)
			mgr.Delete(a.ID)
		}
	})
	return &Sidecar{
		mgr:      mgr,
		channels: channel.NewManager(),
		win:      &mygo.Window{},
	}
}

// stopAll 停掉所有 agent，避免测试结束时仍有 goroutine 跑 mock 流。
func stopAll(t *testing.T, s *Sidecar) {
	t.Helper()
	for _, a := range s.mgr.List() {
		s.mgr.Stop(a.ID)
	}
}

// TC-013 —— ListModelsFor 在 mock / 空 BaseURL 时短路，不发 HTTP。
func TestListModelsForShortCircuit(t *testing.T) {
	tests := []struct {
		name      string
		agentID   string
		cfg       LLMConfig
		globalCfg llm.Config
		want      []string
		wantErr   error
	}{
		{
			name:      "TC-011 explicit mock provider",
			agentID:   "a1",
			cfg:       LLMConfig{Provider: "mock"},
			globalCfg: llm.Config{Provider: "openai", BaseURL: "http://127.0.0.1:1"},
			want:      []string{"mock"},
		},
		{
			name:      "TC-011 empty provider falls back to global mock",
			agentID:   "a1",
			cfg:       LLMConfig{},
			globalCfg: llm.Config{Provider: "mock", Model: "global-m"},
			want:      []string{"mock"},
		},
		{
			name:      "TC-013 openai without baseURL",
			agentID:   "a1",
			cfg:       LLMConfig{Provider: "openai", Model: "gpt-x"},
			globalCfg: llm.Config{Provider: "mock"},
			wantErr:   llm.ErrNotConfigured,
		},
		{
			name:      "TC-013 ollama without baseURL",
			agentID:   "a1",
			cfg:       LLMConfig{Provider: "ollama"},
			globalCfg: llm.Config{Provider: "mock"},
			wantErr:   llm.ErrNotConfigured,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := agent.NewManager(tc.globalCfg, func(string, any) {})
			mgr.Create(tc.agentID, "A1", "r")
			s := &Sidecar{mgr: mgr, channels: channel.NewManager(), win: &mygo.Window{}}

			models, err := s.ListModelsFor(tc.agentID, tc.cfg)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("ListModelsFor(%q, %+v) = (%v, nil), want error %v", tc.agentID, tc.cfg, models, tc.wantErr)
				}
				if err != tc.wantErr {
					t.Errorf("ListModelsFor(%q, %+v): error = %v, want sentinel %v", tc.agentID, tc.cfg, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListModelsFor(%q, %+v): %v", tc.agentID, tc.cfg, err)
			}
			if len(models) != len(tc.want) {
				t.Fatalf("ListModelsFor(%q, %+v) = %v, want %v", tc.agentID, tc.cfg, models, tc.want)
			}
			for i := range tc.want {
				if models[i] != tc.want[i] {
					t.Errorf("ListModelsFor(%q, %+v) = %v, want %v", tc.agentID, tc.cfg, models, tc.want)
				}
			}
		})
	}
}

// TC-001/007 的 Sidecar 层 —— SetAgentWorkDir / SetAgentLLM 返回更新后的 AgentView，
// 且不影响其他会话；GetAgentConfig 反映会话级覆盖。
func TestSidecarSessionConfig(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)
	s.mgr.SetWorkDir("/global")

	a1 := s.CreateAgent(CreateAgentRequest{Name: "A1", Role: "r"})
	a2 := s.CreateAgent(CreateAgentRequest{Name: "A2", Role: "r"})

	v1, err := s.SetAgentWorkDir(a1.ID, "/tmp/a1")
	if err != nil {
		t.Fatalf("SetAgentWorkDir(%q, \"/tmp/a1\"): %v", a1.ID, err)
	}
	if v1.WorkDir != "/tmp/a1" {
		t.Errorf("SetAgentWorkDir(%q): view.WorkDir = %q, want %q", a1.ID, v1.WorkDir, "/tmp/a1")
	}
	if v2, _ := s.mgr.Get(a2.ID); v2.EffectiveWorkDir(s.mgr) != "/global" {
		t.Errorf("SetAgentWorkDir(%q): sibling a2 WorkDir = %q, want fallback %q",
			a1.ID, v2.EffectiveWorkDir(s.mgr), "/global")
	}

	cfg := LLMConfig{Provider: "openai", BaseURL: "http://x", Model: "agent-m"}
	v1, err = s.SetAgentLLM(a1.ID, cfg)
	if err != nil {
		t.Fatalf("SetAgentLLM(%q, %+v): %v", a1.ID, cfg, err)
	}
	if v1.LLM == nil || v1.LLM.Model != "agent-m" {
		t.Errorf("SetAgentLLM(%q): view.LLM = %+v, want Model=agent-m", a1.ID, v1.LLM)
	}
	if got := s.GetAgentConfig(a1.ID); got.Model != "agent-m" {
		t.Errorf("GetAgentConfig(%q).Model = %q, want %q", a1.ID, got.Model, "agent-m")
	}
	if got := s.GetAgentConfig(a2.ID); got.Model != "global-m" {
		t.Errorf("GetAgentConfig(%q).Model = %q, want global fallback %q", a2.ID, got.Model, "global-m")
	}

	// 对不存在的会话必须返回错误，不能 panic。
	if _, err := s.SetAgentWorkDir("NOPE", "/x"); err == nil {
		t.Error("SetAgentWorkDir(\"NOPE\") = nil error, want error")
	}
	if _, err := s.SetAgentLLM("NOPE", cfg); err == nil {
		t.Error("SetAgentLLM(\"NOPE\") = nil error, want error")
	}
}

// TC-022 —— AttachNoteToAgent 追加 system message（含 [知识] + 正文）。
func TestAttachNoteToAgent(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)
	s.kb = newTestKB(t)
	a := s.CreateAgent(CreateAgentRequest{Name: "A1", Role: "r"})

	note, err := s.UpsertNote(KnowledgeNote{Title: "约定", Body: "用 Go 风格"})
	if err != nil {
		t.Fatalf("UpsertNote: %v", err)
	}

	before := len(a.Messages) // a 是 CreateAgent 返回的值拷贝，不再变化
	v, err := s.AttachNoteToAgent(note.ID, a.ID)
	if err != nil {
		t.Fatalf("AttachNoteToAgent(%q, %q): %v", note.ID, a.ID, err)
	}
	if len(v.Messages) != before+1 {
		t.Fatalf("AttachNoteToAgent(%q, %q): len(Messages) = %d, want %d", note.ID, a.ID, len(v.Messages), before+1)
	}
	last := v.Messages[len(v.Messages)-1]
	if last.Role != "system" {
		t.Errorf("AttachNoteToAgent(%q, %q): last role = %q, want %q", note.ID, a.ID, last.Role, "system")
	}
	if !strings.Contains(last.Content, "[知识]") {
		t.Errorf("AttachNoteToAgent(%q, %q): last content = %q, want containing %q", note.ID, a.ID, last.Content, "[知识]")
	}
	if !strings.Contains(last.Content, "用 Go 风格") {
		t.Errorf("AttachNoteToAgent(%q, %q): last content = %q, want containing note body", note.ID, a.ID, last.Content)
	}

	// 异常路径
	if _, err := s.AttachNoteToAgent("ghost", a.ID); err == nil {
		t.Error("AttachNoteToAgent(\"ghost\") = nil error, want error")
	}
	if _, err := s.AttachNoteToAgent(note.ID, "NOPE"); err == nil {
		t.Error("AttachNoteToAgent(note, \"NOPE\") = nil error, want error")
	}
}

// TC-024 —— 0 成员频道仍然记录消息，且 SendToChannel 不报错。
func TestSendToChannelNoMembers(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)

	ch := s.CreateChannel("空频道", nil)
	if err := s.SendToChannel(ch.ID, "user", "你", "hello"); err != nil {
		t.Fatalf("SendToChannel(%q) with 0 members: %v", ch.ID, err)
	}
	got, ok := s.channels.Get(ch.ID)
	if !ok {
		t.Fatalf("Get(%q) not found after SendToChannel", ch.ID)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("SendToChannel(%q): len(Messages) = %d, want 1", ch.ID, len(got.Messages))
	}
	if got.Messages[0].Text != "hello" || got.Messages[0].FromName != "你" {
		t.Errorf("SendToChannel(%q): Messages[0] = %+v, want text=hello fromName=你", ch.ID, got.Messages[0])
	}
}

// TC-026 —— 单个接收者失败不中断其他成员，错误合并返回；消息仍落库。
//
// 为了让"接收者失败"可确定性构造，这里把 channel 成员留成一个已被删除的
// agent ID（Manager.Get 必然失败）——比"把队列灌满再抢时间"更稳定：
// 队列是 32 缓冲且 worker 持续消费，灌满本身是有竞态的。
func TestSendToChannelPartialFailureKeepsOthers(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)

	gone := s.CreateAgent(CreateAgentRequest{Name: "Gone", Role: "r"})
	healthy := s.CreateAgent(CreateAgentRequest{Name: "Healthy", Role: "r"})

	ch := s.CreateChannel("g", []string{gone.ID, healthy.ID})
	// 直接删 manager 里的 agent，但保留频道成员（模拟"成员表与 agent 表不一致"），
	// 这样 SendToChannel 的 Prompt 会稳定返回 "agent ... not found"。
	if ok := s.mgr.Delete(gone.ID); !ok {
		t.Fatalf("mgr.Delete(%q) = false, want true", gone.ID)
	}

	err := s.SendToChannel(ch.ID, "user", "你", "hi")
	if err == nil {
		t.Fatalf("SendToChannel(%q) with a missing recipient: err = nil, want merged error", ch.ID)
	}
	if !strings.Contains(err.Error(), "部分成员未能接收") {
		t.Errorf("SendToChannel(%q): error = %q, want containing %q", ch.ID, err, "部分成员未能接收")
	}
	if !strings.Contains(err.Error(), gone.ID) {
		t.Errorf("SendToChannel(%q): error = %q, want it to mention missing agent %q", ch.ID, err, gone.ID)
	}

	// 消息必须已落库（即使有成员失败）。
	got, ok := s.channels.Get(ch.ID)
	if !ok {
		t.Fatalf("Get(%q) not found after SendToChannel", ch.ID)
	}
	if len(got.Messages) != 1 {
		t.Errorf("SendToChannel(%q) with a failing member: len(Messages) = %d, want 1", ch.ID, len(got.Messages))
	}

	// 健康的成员必须仍然收到消息（错误被合并而非提前返回）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, _ := s.mgr.Get(healthy.ID)
		msgs := messagesOf(a)
		if len(msgs) > 1 && msgs[len(msgs)-1].Role == "user" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	a, _ := s.mgr.Get(healthy.ID)
	t.Fatalf("SendToChannel(%q): healthy agent %q never received the prompt (Messages=%+v)",
		ch.ID, healthy.ID, messagesOf(a))
}

// TC-028 —— 删 agent 后频道成员被清理，List 中也不再有该 agent。
func TestDeleteAgentRemovesChannelMember(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)

	a1 := s.CreateAgent(CreateAgentRequest{Name: "A1", Role: "r"})
	a2 := s.CreateAgent(CreateAgentRequest{Name: "A2", Role: "r"})
	ch := s.CreateChannel("g", []string{a1.ID, a2.ID})

	if ok := s.DeleteAgent(a1.ID); !ok {
		t.Fatalf("DeleteAgent(%q) = false, want true", a1.ID)
	}

	got, ok := s.channels.Get(ch.ID)
	if !ok {
		t.Fatalf("Get(%q) not found after DeleteAgent", ch.ID)
	}
	if len(got.Members) != 1 || got.Members[0] != a2.ID {
		t.Errorf("DeleteAgent(%q): members = %v, want [%s]", a1.ID, got.Members, a2.ID)
	}
	for _, a := range s.ListAgents() {
		if a.ID == a1.ID {
			t.Errorf("DeleteAgent(%q): agent still listed", a1.ID)
		}
	}
}

// toXxx 纯函数：畸形负载返回 ok=false 而非 panic（win==nil 可测的契约）。
func TestEventPayloadDecoders(t *testing.T) {
	malformed := []any{"string", 42, nil, struct{}{}}
	for _, payload := range malformed {
		if v, ok := toAgentCreated(payload); ok {
			t.Errorf("toAgentCreated(%#v) = (%+v, true), want ok=false", payload, v)
		}
		if v, ok := toAgentDeleted(payload); ok {
			t.Errorf("toAgentDeleted(%#v) = (%q, true), want ok=false", payload, v)
		}
		if v, ok := toAgentStatus(payload); ok {
			t.Errorf("toAgentStatus(%#v) = (%+v, true), want ok=false", payload, v)
		}
		if v, ok := toAgentDelta(payload); ok {
			t.Errorf("toAgentDelta(%#v) = (%+v, true), want ok=false", payload, v)
		}
		if v, ok := toAgentMessage(payload); ok {
			t.Errorf("toAgentMessage(%#v) = (%+v, true), want ok=false", payload, v)
		}
		if v, ok := toAgentError(payload); ok {
			t.Errorf("toAgentError(%#v) = (%+v, true), want ok=false", payload, v)
		}
		if v, ok := toToolStart(payload); ok {
			t.Errorf("toToolStart(%#v) = (%+v, true), want ok=false", payload, v)
		}
	}
}

// agent.created 负载必须把 workDir / llmCfg 映射到 AgentView。
func TestToAgentCreatedMapsSessionConfig(t *testing.T) {
	cfg := &llm.Config{Provider: "openai", BaseURL: "http://x", Model: "m"}
	payload := map[string]any{
		"id":        "a1",
		"name":      "A1",
		"role":      "r",
		"color":     "#fff",
		"status":    "idle",
		"createdAt": int64(1700000000),
		"messages":  []llm.Message{{Role: "user", Content: "hi"}},
		"queueLen":  0,
		"backend":   "mock",
		"workDir":   "/tmp/w",
		"llmCfg":    cfg,
	}

	v, ok := toAgentCreated(payload)
	if !ok {
		t.Fatalf("toAgentCreated(%+v) = ok=false, want true", payload)
	}
	if v.WorkDir != "/tmp/w" {
		t.Errorf("toAgentCreated: WorkDir = %q, want %q", v.WorkDir, "/tmp/w")
	}
	if v.LLM == nil || v.LLM.Model != "m" || v.LLM.Provider != "openai" {
		t.Errorf("toAgentCreated: LLM = %+v, want %+v", v.LLM, cfg)
	}
	if v.CreatedAt != 1700000000 {
		t.Errorf("toAgentCreated: CreatedAt = %d, want 1700000000", v.CreatedAt)
	}
	if len(v.Messages) != 1 || v.Messages[0].Content != "hi" {
		t.Errorf("toAgentCreated: Messages = %+v, want one user message", v.Messages)
	}
}

// nil llmCfg 必须映射成 nil（前端据此显示"跟随全局"）。
func TestToAgentCreatedNilLLM(t *testing.T) {
	v, ok := toAgentCreated(map[string]any{"id": "a1", "llmCfg": nil, "messages": []llm.Message{}})
	if !ok {
		t.Fatal("toAgentCreated with llmCfg=nil = ok=false, want true")
	}
	if v.LLM != nil {
		t.Errorf("toAgentCreated with llmCfg=nil: LLM = %+v, want nil", v.LLM)
	}
}

// agent.message 解码必须带出 tokensIn/tokensOut。
func TestToAgentMessageTokens(t *testing.T) {
	payload := map[string]any{
		"id":        "a1",
		"message":   llm.Message{Role: "assistant", Content: "ok"},
		"tokensIn":  12,
		"tokensOut": 34,
	}
	evt, ok := toAgentMessage(payload)
	if !ok {
		t.Fatalf("toAgentMessage(%+v) = ok=false, want true", payload)
	}
	if evt.TokensIn != 12 || evt.TokensOut != 34 {
		t.Errorf("toAgentMessage: tokens = (%d, %d), want (12, 34)", evt.TokensIn, evt.TokensOut)
	}
	if evt.Message.Content != "ok" {
		t.Errorf("toAgentMessage: Message.Content = %q, want %q", evt.Message.Content, "ok")
	}
}

// knowledgeProvider 适配层：限制注入条数、空正文只出标题、nil 安全。
func TestKnowledgeProviderNotesForSystemPrompt(t *testing.T) {
	kb := newTestKB(t)

	var none knowledgeProvider
	if got := none.NotesForSystemPrompt(); got != nil {
		t.Errorf("zero knowledgeProvider.NotesForSystemPrompt() = %v, want nil", got)
	}

	if got := (&knowledgeProvider{store: kb}).NotesForSystemPrompt(); got != nil {
		t.Errorf("empty store NotesForSystemPrompt() = %v, want nil (no empty section)", got)
	}

	for i := 0; i < maxKnowledgeLines+4; i++ {
		if _, err := kb.Upsert(knowledgeNoteAt(i)); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}
	lines := (&knowledgeProvider{store: kb}).NotesForSystemPrompt()
	if len(lines) != maxKnowledgeLines {
		t.Fatalf("NotesForSystemPrompt(): len = %d, want capped %d", len(lines), maxKnowledgeLines)
	}
	for i, line := range lines {
		if line == "" {
			t.Errorf("NotesForSystemPrompt(): line %d is empty", i)
		}
	}
}

// TC-029 —— SendToChannel 只推频道消息，不重复创建频道；
// 成员回复走 agent.message（频道 Messages 只有用户那一条）。
func TestSendToChannelDoesNotDuplicateChannel(t *testing.T) {
	s := newTestSidecar(t)
	defer stopAll(t, s)

	a := s.CreateAgent(CreateAgentRequest{Name: "A1", Role: "r"})
	ch := s.CreateChannel("g", []string{a.ID})

	if got := len(s.ListChannels()); got != 1 {
		t.Fatalf("CreateChannel: len(ListChannels()) = %d, want 1", got)
	}
	if err := s.SendToChannel(ch.ID, "user", "你", "hi"); err != nil {
		t.Fatalf("SendToChannel(%q): %v", ch.ID, err)
	}
	if got := len(s.ListChannels()); got != 1 {
		t.Errorf("SendToChannel(%q): len(ListChannels()) = %d, want 1 (no duplicate channel)", ch.ID, got)
	}

	got, _ := s.channels.Get(ch.ID)
	if len(got.Messages) != 1 {
		t.Fatalf("SendToChannel(%q): len(Messages) = %d, want 1 (only the user message)", ch.ID, len(got.Messages))
	}
	if got.Messages[0].FromID != "user" {
		t.Errorf("SendToChannel(%q): Messages[0].FromID = %q, want %q", ch.ID, got.Messages[0].FromID, "user")
	}

	// 成员的回复进入它自己的对话历史，而不是频道。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a2, _ := s.mgr.Get(a.ID)
		if msgs := messagesOf(a2); len(msgs) >= 3 { // system + [ch] user + assistant
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	a2, _ := s.mgr.Get(a.ID)
	t.Fatalf("agent %q never produced an assistant reply (Messages=%+v)", a.ID, messagesOf(a2))
}
