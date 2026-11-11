package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// openAICompat 走 /v1/chat/completions 流式接口，覆盖 Ollama / OpenAI / DeepSeek / Claude 网关。
type openAICompat struct {
	name string
	cfg  Config
}

func (c *openAICompat) Name() string { return c.name }

func (c *openAICompat) endpoint() string {
	base := strings.TrimSuffix(c.cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/chat/completions"
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
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *openAICompat) Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error) {
	body, err := json.Marshal(chatReq{Model: c.cfg.Model, Messages: msgs, Stream: true})
	if err != nil {
		return nil, fmt.Errorf("llm: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: new request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
	}

	httpClient := &http.Client{Timeout: 0}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: do: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("llm: upstream %d", resp.StatusCode)
	}

	out := make(chan Chunk, 16)
	go func() {
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
					select {
					case out <- Chunk{Delta: ch.Delta.Content}:
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
