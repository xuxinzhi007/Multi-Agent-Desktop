package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

// Turn 是模型一次响应的解析结果。
//
// 放在 llm 包而不是 loop 包：它是「模型说了什么」的表示，
// 与「怎么治理循环」无关。loop 依赖它，反之不成立。
type Turn struct {
	// Text 是本轮文字输出（可能为空，也可能是最终答案）。
	Text string
	// Reasoning 是思考模式返回的推理，和 Text 分开。
	Reasoning string
	// Calls 是模型想执行的工具。为空表示它认为任务已经完成。
	Calls []tool.Call
}

// Delta 是流式 Turn 里已经切好的一段增量。
// 工具参数不在这里：碎片要等这一轮流结束、拼成完整 JSON 之后才进入 Turn.Calls。
type Delta struct {
	Text      string
	Reasoning string
}

// ChatModel 是给 agent loop 用的一次性（非流式）调用。
//
// 为什么不复用 llm.Client（流式接口）：
// 循环需要的是「一轮完整结果」——文字 + 工具调用。流式解析
// 属于 provider 适配，不该泄漏进治理逻辑。两者并存，
// 聊天界面走流式，loop 走这个，各自简单。
type ChatModel struct {
	cfg    Config
	client *http.Client
}

// NewChatModel 造一个支持 function calling 的调用器。
func NewChatModel(cfg Config) *ChatModel {
	cfg = ApplyDefaults(cfg)
	return &ChatModel{cfg: cfg, client: httpClient}
}

func boundChat(ctx context.Context, cfg Config) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, chatBudget(cfg))
}

// chatBudget 是这一轮等上游的上限。
// 高和更高才放宽到 5 分钟。中等及以下保持 180 秒，避免普通问题空等。
func chatBudget(cfg Config) time.Duration {
	if !ThinkingEnabled(cfg) {
		return 180 * time.Second
	}
	switch wireEffort(cfg) {
	case "high", "xhigh", "max":
		return 5 * time.Minute
	default:
		return 180 * time.Second
	}
}

// Effort 是这一轮的推理强度。空值和 on 都是 medium。
func Effort(cfg Config) string {
	switch cfg.Thinking {
	case "off", "low", "medium", "high", "xhigh", "max":
		return cfg.Thinking
	default:
		return "medium"
	}
}

// ModelReasons 表示这个模型带思考。空思考档也算开着；显式关掉不算。
func ModelReasons(provider, model string) bool {
	return ThinkingEnabled(Config{Provider: provider, Model: model})
}

// ThinkingEnabled 表示这次请求会把思考发给上游。
// 显式 off 关掉。qoder 只有模型本身支持推理时才开。
func ThinkingEnabled(cfg Config) bool {
	if Effort(cfg) == "off" {
		return false
	}
	switch cfg.Provider {
	case "deepseek":
		return true
	case "qoder":
		return qoderModelByID(cfg.Model).reasoning
	default:
		return false
	}
}

func wireEffort(cfg Config) string {
	if cfg.Provider == "qoder" {
		m := qoderModelByID(cfg.Model)
		if len(m.efforts) > 0 {
			want := Effort(cfg)
			for _, e := range m.efforts {
				if e == want {
					return e
				}
			}
			if m.defaultEffort != "" {
				return m.defaultEffort
			}
			return m.efforts[0]
		}
	}
	switch Effort(cfg) {
	case "low", "high", "xhigh", "max":
		return Effort(cfg)
	default:
		return "medium"
	}
}

func thinkingLabel(cfg Config) string {
	if !ThinkingEnabled(cfg) {
		return "思考关"
	}
	switch wireEffort(cfg) {
	case "low":
		return "快"
	case "high":
		return "高"
	case "xhigh":
		return "更高"
	case "max":
		return "最高"
	default:
		return "中"
	}
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Turn 是一轮模型调用，返回文字和要执行的工具。
//
// 这里把 OpenAI 的 tool_calls 归一化成结构化的 Call，
// 模型层的格式差异到此为止 —— 循环和工具层不需要知道协议细节。
func (c *ChatModel) Turn(ctx context.Context, msgs []Message, tools []tool.Spec) (Turn, error) {
	ctx, cancel := boundChat(ctx, c.cfg)
	defer cancel()
	return AnswerDespiteImages(msgs, func(msgs []Message) (Turn, error) {
		return c.turnNow(ctx, msgs, tools)
	})
}

func (c *ChatModel) turnNow(ctx context.Context, msgs []Message, tools []tool.Spec) (Turn, error) {
	if c.cfg.Provider == "qoder" {
		return qoderTurn(ctx, c.client, c.cfg, msgs, tools)
	}
	resp, err := c.postChat(ctx, msgs, tools, false)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Turn{}, fmt.Errorf("llm: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Turn{}, fmt.Errorf("llm: upstream %d: %s", resp.StatusCode, truncateForError(payload))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content          string         `json:"content"`
				ReasoningContent string         `json:"reasoning_content"`
				ToolCalls        []wireToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return Turn{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if out.Error != nil {
		return Turn{}, fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Turn{}, fmt.Errorf("llm: empty choices")
	}

	msg := out.Choices[0].Message
	turn := Turn{Text: msg.Content, Reasoning: msg.ReasoningContent}
	for _, tc := range msg.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		turn.Calls = append(turn.Calls, tool.Call{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(args),
		})
	}
	return turn, nil
}

// TurnStream 是一轮带 tools 的流式调用。
// 文本和推理增量通过 onDelta 立刻送出。tool_calls 按 index 拼完，
// arguments 必须是完整 JSON，才放进返回的 Turn。拼不齐、上游报错或 ctx 取消都返回 error，不带半截参数。
func (c *ChatModel) TurnStream(ctx context.Context, msgs []Message, tools []tool.Spec, onDelta func(Delta)) (Turn, error) {
	ctx, cancel := boundChat(ctx, c.cfg)
	defer cancel()
	if onDelta == nil {
		onDelta = func(Delta) {}
	}
	return AnswerDespiteImages(msgs, func(msgs []Message) (Turn, error) {
		return c.turnStreamNow(ctx, msgs, tools, onDelta)
	})
}

func (c *ChatModel) turnStreamNow(ctx context.Context, msgs []Message, tools []tool.Spec, onDelta func(Delta)) (Turn, error) {
	if c.cfg.Provider == "qoder" {
		return qoderTurnStream(ctx, c.client, c.cfg, msgs, tools, onDelta)
	}
	resp, err := c.postChat(ctx, msgs, tools, true)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return Turn{}, fmt.Errorf("llm: upstream %d: %s", resp.StatusCode, truncateForError(payload))
	}
	return readToolStream(ctx, resp.Body, onDelta)
}

func (c *ChatModel) postChat(ctx context.Context, msgs []Message, tools []tool.Spec, stream bool) (*http.Response, error) {
	req := map[string]any{
		"model":    c.cfg.Model,
		"messages": toWire(msgs, thinkingOn(c.cfg)),
	}
	if stream {
		req["stream"] = true
	}
	applyThinking(req, c.cfg)
	if len(tools) > 0 {
		wt := make([]wireTool, 0, len(tools))
		for _, s := range tools {
			schema := s.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			wt = append(wt, wireTool{
				Type: "function",
				Function: wireFunction{
					Name:        s.Name,
					Description: s.Description,
					Parameters:  schema,
				},
			})
		}
		req["tools"] = wt
		req["tool_choice"] = "auto"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	base := strings.TrimSuffix(c.cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	url := base + "/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	httpReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	httpReq.Header.Set("content-type", "application/json")
	if stream {
		httpReq.Header.Set("accept", "text/event-stream")
	}
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := do(c.client, httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: do: %w", err)
	}
	return resp, nil
}

type toolAcc struct {
	id   string
	name string
	args string
}

func foldToolDelta(acc []toolAcc, index int, id, name, args string) ([]toolAcc, error) {
	if index < 0 || index > 32 {
		return nil, fmt.Errorf("llm: tool index %d out of range", index)
	}
	for len(acc) <= index {
		acc = append(acc, toolAcc{})
	}
	slot := &acc[index]
	slot.id += id
	slot.name += name
	slot.args += args
	return acc, nil
}

func callsFromAcc(acc []toolAcc) ([]tool.Call, error) {
	var out []tool.Call
	for _, tc := range acc {
		if tc.id == "" && tc.name == "" && strings.TrimSpace(tc.args) == "" {
			continue
		}
		if tc.name == "" {
			return nil, fmt.Errorf("llm: tool call missing name")
		}
		args := strings.TrimSpace(tc.args)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return nil, fmt.Errorf("llm: tool %s arguments are incomplete", tc.name)
		}
		out = append(out, tool.Call{
			ID:    tc.id,
			Name:  tc.name,
			Input: json.RawMessage(args),
		})
	}
	return out, nil
}

func readToolStream(ctx context.Context, r io.Reader, onDelta func(Delta)) (Turn, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var turn Turn
	var acc []toolAcc
	saw := false
	for sc.Scan() {
		if ctx.Err() != nil {
			return Turn{}, ctx.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Turn{}, fmt.Errorf("llm: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		saw = true
		d := chunk.Choices[0].Delta
		if d.Content != "" || d.ReasoningContent != "" {
			onDelta(Delta{Text: d.Content, Reasoning: d.ReasoningContent})
			turn.Text += d.Content
			turn.Reasoning += d.ReasoningContent
		}
		for _, tc := range d.ToolCalls {
			var err error
			acc, err = foldToolDelta(acc, tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				return Turn{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return Turn{}, ctx.Err()
		}
		return Turn{}, fmt.Errorf("llm: read stream: %w", err)
	}
	if ctx.Err() != nil {
		return Turn{}, ctx.Err()
	}
	calls, err := callsFromAcc(acc)
	if err != nil {
		return Turn{}, err
	}
	if !saw && len(calls) == 0 && turn.Text == "" && turn.Reasoning == "" {
		return Turn{}, fmt.Errorf("llm: empty stream")
	}
	turn.Calls = calls
	return turn, nil
}

func thinkingOn(cfg Config) bool {
	return ThinkingEnabled(cfg)
}

func applyThinking(req map[string]any, cfg Config) {
	if cfg.Provider != "deepseek" {
		return
	}
	if !ThinkingEnabled(cfg) {
		req["thinking"] = map[string]string{"type": "disabled"}
		return
	}
	req["thinking"] = map[string]string{"type": "enabled"}
	req["reasoning_effort"] = wireEffort(cfg)
}

func toWire(msgs []Message, echoReasoning bool) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		w := wireMessage{Role: m.Role, Content: wireContent(m), ToolCallID: m.ToolID}
		if echoReasoning {
			w.ReasoningContent = m.Reasoning
		}
		for _, tc := range m.ToolCalls {
			wtc := wireToolCall{ID: tc.ID, Type: "function"}
			wtc.Function.Name = tc.Name
			wtc.Function.Arguments = string(tc.Input)
			w.ToolCalls = append(w.ToolCalls, wtc)
		}
		out = append(out, w)
	}
	return out
}

func wireContent(m Message) any {
	var textIn bool
	var pixels int
	for _, b := range m.Blocks {
		if b.Text != "" {
			textIn = true
		}
		if len(b.Data) > 0 {
			pixels++
		}
	}
	if pixels == 0 {
		return m.Content
	}
	var parts []map[string]any
	if !textIn && strings.TrimSpace(m.Content) != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	n := 0
	for _, b := range m.Blocks {
		if b.Text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": b.Text})
		}
		if len(b.Data) == 0 {
			continue
		}
		n++
		parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprintf("图%d", n)})
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": dataURL(b)},
		})
	}
	if len(parts) == 0 {
		return m.Content
	}
	return parts
}

func truncateForError(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
