package llm

import (
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
	// 与 New 共用同一套默认值：llamacpp 之类不填 BaseURL 也要能跑通
	cfg = ApplyDefaults(cfg)
	return &ChatModel{
		cfg: cfg,
		// 有超时是硬要求：没有超时的 http.Client 会让循环永久挂住，
		// 而「停止」按钮此时什么都做不了。
		client: &http.Client{Timeout: 180 * time.Second},
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
	Content          string         `json:"content,omitempty"`
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
	echoReasoning := thinkingOn(c.cfg)
	req := map[string]any{
		"model":    c.cfg.Model,
		"messages": toWire(msgs, echoReasoning),
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
		return Turn{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	base := strings.TrimSuffix(c.cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	url := base + "/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Turn{}, fmt.Errorf("llm: new request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return Turn{}, fmt.Errorf("llm: do: %w", err)
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

func thinkingOn(cfg Config) bool {
	return cfg.Provider == "deepseek" && cfg.Thinking != "off"
}

func applyThinking(req map[string]any, cfg Config) {
	if cfg.Provider != "deepseek" {
		return
	}
	if cfg.Thinking == "off" {
		req["thinking"] = map[string]string{"type": "disabled"}
		return
	}
	req["thinking"] = map[string]string{"type": "enabled"}
	req["reasoning_effort"] = "high"
}

func toWire(msgs []Message, echoReasoning bool) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		w := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolID}
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

func truncateForError(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
