package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// openAICompat 走 /v1/chat/completions 流式接口，覆盖 Ollama / OpenAI / DeepSeek / Claude 网关。
type openAICompat struct {
	name string
	cfg  Config

	// client 在实例内复用，不能每次请求 new 一个。
	// 每次新建会把上一个 client 的空闲连接池丢在服务端，
	// 服务端关掉那条连接后，下一个 client 恰好复用到它，
	// 就会随机出现 `http: server closed idle connection` ——
	// 用户看到的是「偶尔发一条就没反应」。
	client *http.Client
}

func (c *openAICompat) Name() string { return c.name }

func (c *openAICompat) endpoint() string { return ChatURL(c.cfg) }

// ChatURL 拼对话地址。豆包方舟走 /api/v3/chat/completions，
// 其它 OpenAI 兼容服务走 /v1/chat/completions。
func ChatURL(cfg Config) string {
	base := doubaoBase(cfg)
	if cfg.Provider == "doubao" {
		return base + "/chat/completions"
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/chat/completions"
}

// ModelsURL 拼模型列表地址。
func ModelsURL(cfg Config) string {
	base := doubaoBase(cfg)
	if cfg.Provider == "doubao" {
		return base + "/models"
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/models"
}

func doubaoBase(cfg Config) string {
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.Provider == "doubao" && !strings.Contains(base, "/api/") {
		base += "/api/v3"
	}
	return base
}

// do 发出请求，并在「连接层就断了」时重试一次。
//
// 背景：服务端会在空闲后主动关闭 keep-alive 连接，而客户端可能
// 恰好选中一条已经死掉的连接，于是一次请求直接失败。
// Go 只对幂等的 GET 自动重试，POST 不重试，所以这里自己补。
//
// 安全性：只重试「请求还没被服务端处理」的错误类型。
// 实测这些错误下服务端计数不增长，即模型没有真的被调用，
// 重试不会让同一句话生成两遍或扣两次费用。
// 流已经建立之后的错误一律不重试 —— 那会重复输出。
func do(hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err == nil || !isStaleConnErr(err) || req.Context().Err() != nil {
		return resp, err
	}
	// body 已被读过一次，必须重建才能重发
	if req.GetBody == nil {
		return resp, err
	}
	body, berr := req.GetBody()
	if berr != nil {
		return resp, err
	}
	retry := req.Clone(req.Context())
	retry.Body = body
	return hc.Do(retry)
}

// isStaleConnErr 判断是不是「连接在请求被处理前就没了」。
func isStaleConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "server closed idle connection") ||
		strings.Contains(s, "connection reset by peer")
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type chatResp struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ListModels 调 OpenAI /models 接口获取可用模型列表。
func ListModels(ctx context.Context, cfg Config) ([]string, error) {
	if cfg.Provider == DoubaoCLIProvider {
		metas, err := ListDoubaoCLIModels(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(metas))
		for i, m := range metas {
			names[i] = m.Name
		}
		return names, nil
	}
	if cfg.Provider == "qoder" {
		cfg = ApplyDefaults(cfg)
		if cfg.APIKey == "" {
			if saved, ok := LocalQoder(); ok {
				cfg.APIKey = saved.APIKey
			}
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("%w", ErrMissingAPIKey)
		}
		models := qoderResolve(ctx, cfg)
		names := make([]string, len(models))
		for i, m := range models {
			names[i] = m.id
		}
		return names, nil
	}
	url := ModelsURL(cfg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	if cfg.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: upstream %d", resp.StatusCode)
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("llm: decode: %w", err)
	}

	var models []string
	for _, m := range data.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

func (c *openAICompat) Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error) {
	// 需要鉴权的 provider 缺 Key 时立刻失败。
	// 否则请求会被发出去（要么被拒要么挂起），用户只看到「排队中」不知原因。
	// ollama / llamacpp 是本地服务，不需要 Key。
	if c.cfg.APIKey == "" && !IsLocalProvider(c.name) {
		return nil, fmt.Errorf("%w（当前 provider: %s）", ErrMissingAPIKey, c.name)
	}

	body, err := json.Marshal(chatReq{Model: c.cfg.Model, Messages: msgs, Stream: true})
	if err != nil {
		return nil, fmt.Errorf("llm: marshal: %w", err)
	}

	// GetBody 必须给出：连接层重试时要靠它把请求体重建出来。
	// 不设置的话，`do` 的重试分支只能放弃重试。
	bodyReader := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	req.GetBody = bodyReader
	req.Header.Set("content-type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
	}

	// 由 ctx 统一控制超时：调用方给了 deadline 就用它，否则兜底 StreamTimeout。
	// 不能用 Timeout:0 —— 上游挂起时 Do() 永不返回，worker 会被永久占住。
	//
	// cancel 必须由读 body 的 goroutine 释放，不能用 defer：
	// defer 在 Stream 返回（把 channel 交给调用方）的瞬间就执行，
	// 而那时 body 才刚开始读。请求 ctx 被取消 → 后续 Scan 全部失败，
	// 返回一个空回复且没有 error，界面上就是「发了消息但没有任何回应」。
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, StreamTimeout)
	} else {
		cancel = func() {}
	}
	req = req.WithContext(ctx)

	httpClient := c.client
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	resp, err := do(httpClient, req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("llm: do: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("llm: upstream %d", resp.StatusCode)
	}

	out := make(chan Chunk, 16)
	go func() {
		defer cancel()
		defer close(out)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				select {
				case out <- Chunk{Done: true}:
				case <-ctx.Done():
				}
				return
			}

			var r chatResp
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				continue
			}
			if r.Error != nil {
				select {
				case out <- Chunk{Err: r.Error.Message}:
				case <-ctx.Done():
				}
				return
			}
			for _, ch := range r.Choices {
				if ch.Delta.Content != "" {
					chunk := Chunk{Delta: ch.Delta.Content}
					if r.Usage != nil {
						chunk.TokensIn = r.Usage.PromptTokens
						chunk.TokensOut = r.Usage.CompletionTokens
					}
					select {
					case out <- chunk:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			select {
			case out <- Chunk{Err: err.Error()}:
			case <-time.After(time.Second):
			}
		}
		select {
		case out <- Chunk{Done: true}:
		case <-ctx.Done():
		}
	}()

	return out, nil
}
