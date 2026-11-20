package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

const (
	DefaultQoderBaseURL = "https://api3.qoder.sh"
	DefaultQoderModel   = "DeepSeek-V4-Pro"
	qoderChatPath       = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
)

var qoderRefreshURL = "https://center.qoder.sh/algo/api/v3/user/refresh_token"

type qoderModel struct {
	id        string
	key       string
	reasoning bool
}

var qoderModels = []qoderModel{
	{id: "Auto", key: "auto", reasoning: true},
	{id: "Ultimate", key: "ultimate", reasoning: true},
	{id: "Performance", key: "performance", reasoning: true},
	{id: "Efficient", key: "efficient"},
	{id: "Lite", key: "lite"},
	{id: "Qwen3.7Plus", key: "qmodel"},
	{id: "Cantus", key: "cmodel", reasoning: true},
	{id: "Qwen3.8-Max", key: "qmodel_preview", reasoning: true},
	{id: "Qwen3.7-Max", key: "qmodel_latest"},
	{id: "DeepSeek-V4-Pro", key: "dmodel", reasoning: true},
	{id: "DeepSeek-V4-Flash", key: "dfmodel", reasoning: true},
	{id: "GLM-5.2", key: "gm51model", reasoning: true},
	{id: "Kimi-K2.7-Code", key: "kmodel"},
	{id: "Kimi-K3", key: "kmodel_latest"},
}

func qoderModelByID(id string) qoderModel {
	if id == "" {
		id = DefaultQoderModel
	}
	for _, m := range qoderModels {
		if m.id == id || m.key == id {
			return m
		}
	}
	return qoderModel{id: id, key: id}
}

func QoderModelNames() []string {
	out := make([]string, len(qoderModels))
	for i, m := range qoderModels {
		out[i] = m.id
	}
	return out
}

type qoderClient struct {
	cfg    Config
	client *http.Client
}

func (c *qoderClient) Name() string { return "qoder" }

func (c *qoderClient) Stream(ctx context.Context, msgs []Message) (<-chan Chunk, error) {
	if c.cfg.APIKey == "" {
		if cfg, ok := LocalQoder(); ok {
			c.cfg.APIKey = cfg.APIKey
		}
	}
	if c.cfg.APIKey == "" {
		return nil, fmt.Errorf("%w（当前 provider: qoder）", ErrMissingAPIKey)
	}
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, StreamTimeout)
	} else {
		cancel = func() {}
	}
	resp, err := qoderPost(ctx, c.client, c.cfg, msgs, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan Chunk, 16)
	go func() {
		defer cancel()
		defer close(out)
		defer resp.Body.Close()
		qoderRead(ctx, resp.Body, func(d qoderDelta) bool {
			if d.err != "" {
				select {
				case out <- Chunk{Err: d.err}:
				case <-ctx.Done():
				}
				return false
			}
			if d.content != "" {
				select {
				case out <- Chunk{Delta: d.content, TokensIn: d.tokensIn, TokensOut: d.tokensOut}:
				case <-ctx.Done():
					return false
				}
			}
			if d.done {
				select {
				case out <- Chunk{Done: true, TokensIn: d.tokensIn, TokensOut: d.tokensOut}:
				case <-ctx.Done():
				}
				return false
			}
			return true
		})
	}()
	return out, nil
}

func qoderTurn(ctx context.Context, client *http.Client, cfg Config, msgs []Message, tools []tool.Spec) (Turn, error) {
	if cfg.APIKey == "" {
		if saved, ok := LocalQoder(); ok {
			cfg.APIKey = saved.APIKey
		}
	}
	resp, err := qoderPost(ctx, client, cfg, msgs, tools)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	var turn Turn
	var acc []qoderToolAcc
	var turnErr error
	err = qoderRead(ctx, resp.Body, func(d qoderDelta) bool {
		if d.err != "" {
			turnErr = fmt.Errorf("qoder: %s", d.err)
			return false
		}
		turn.Text += d.content
		turn.Reasoning += d.reasoning
		acc = qoderMergeTools(acc, d.tools)
		return !d.done
	})
	if turnErr != nil {
		return Turn{}, turnErr
	}
	if err != nil {
		return Turn{}, err
	}
	for _, tc := range acc {
		args := strings.TrimSpace(tc.args)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			args = "{}"
		}
		turn.Calls = append(turn.Calls, tool.Call{ID: tc.id, Name: tc.name, Input: json.RawMessage(args)})
	}
	return turn, nil
}

func qoderTurnStream(ctx context.Context, client *http.Client, cfg Config, msgs []Message, tools []tool.Spec, onDelta func(Delta)) (Turn, error) {
	if onDelta == nil {
		onDelta = func(Delta) {}
	}
	if cfg.APIKey == "" {
		if saved, ok := LocalQoder(); ok {
			cfg.APIKey = saved.APIKey
		}
	}
	resp, err := qoderPost(ctx, client, cfg, msgs, tools)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	var turn Turn
	var acc []qoderToolAcc
	var turnErr error
	err = qoderRead(ctx, resp.Body, func(d qoderDelta) bool {
		if d.err != "" {
			turnErr = fmt.Errorf("qoder: %s", d.err)
			return false
		}
		if d.content != "" || d.reasoning != "" {
			onDelta(Delta{Text: d.content, Reasoning: d.reasoning})
		}
		turn.Text += d.content
		turn.Reasoning += d.reasoning
		acc = qoderMergeTools(acc, d.tools)
		return !d.done
	})
	if ctx.Err() != nil {
		return Turn{}, ctx.Err()
	}
	if turnErr != nil {
		return Turn{}, turnErr
	}
	if err != nil {
		return Turn{}, err
	}
	for _, tc := range acc {
		if tc.id == "" && tc.name == "" && strings.TrimSpace(tc.args) == "" {
			continue
		}
		if tc.name == "" {
			return Turn{}, fmt.Errorf("llm: tool call missing name")
		}
		args := strings.TrimSpace(tc.args)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return Turn{}, fmt.Errorf("llm: tool %s arguments are incomplete", tc.name)
		}
		turn.Calls = append(turn.Calls, tool.Call{ID: tc.id, Name: tc.name, Input: json.RawMessage(args)})
	}
	return turn, nil
}

type qoderDelta struct {
	content   string
	reasoning string
	tools     []qoderToolDelta
	err       string
	done      bool
	tokensIn  int
	tokensOut int
}

type qoderToolDelta struct {
	index int
	id    string
	name  string
	args  string
}

type qoderToolAcc struct {
	id   string
	name string
	args string
}

func qoderMergeTools(acc []qoderToolAcc, deltas []qoderToolDelta) []qoderToolAcc {
	for _, d := range deltas {
		for len(acc) <= d.index {
			acc = append(acc, qoderToolAcc{})
		}
		slot := &acc[d.index]
		if d.id != "" {
			slot.id = d.id
		}
		if d.name != "" {
			slot.name = d.name
		}
		slot.args += d.args
	}
	return acc
}

func qoderPost(ctx context.Context, client *http.Client, cfg Config, msgs []Message, tools []tool.Spec) (*http.Response, error) {
	ident, err := qoderIdentity(cfg.APIKey)
	if err != nil {
		return nil, err
	}
	model := qoderModelByID(cfg.Model)
	thinking := model.reasoning && cfg.Thinking != "off"
	body, err := qoderRequestBody(model, thinking, msgs, tools)
	if err != nil {
		return nil, err
	}
	encoded := qoderEncode(body)
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultQoderBaseURL
	}
	chatURL := base + qoderChatPath
	headers, err := qoderHeaders(encoded, chatURL, ident)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("x-model-key", model.key)
	req.Header.Set("x-model-source", "system")
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		resp.Body.Close()
		return nil, fmt.Errorf("qoder: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return resp, nil
}

func qoderRequestBody(model qoderModel, thinking bool, msgs []Message, tools []tool.Spec) ([]byte, error) {
	wire := make([]map[string]any, 0, len(msgs))
	lastUser := ""
	var imageURLs []string
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if len(b.Data) == 0 {
				continue
			}
			imageURLs = append(imageURLs, dataURL(b))
		}
		item := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "user" && m.Content != "" {
			lastUser = m.Content
		}
		if m.Role == "tool" {
			item["tool_call_id"] = m.ToolID
		}
		if m.Reasoning != "" {
			item["reasoning_content"] = m.Reasoning
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]string{
						"name":      tc.Name,
						"arguments": string(tc.Input),
					},
				})
			}
			item["tool_calls"] = calls
			if m.Content == "" {
				item["content"] = " "
			}
		}
		wire = append(wire, item)
	}
	var wireTools []wireTool
	for _, s := range tools {
		schema := s.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wireTools = append(wireTools, wireTool{
			Type:     "function",
			Function: wireFunction{Name: s.Name, Description: s.Description, Parameters: schema},
		})
	}
	if wireTools == nil {
		wireTools = []wireTool{}
	}
	params := map[string]any{"max_tokens": 8192, "enable_thinking": thinking}
	if thinking {
		params["reasoning_effort"] = "high"
	}
	cfg := map[string]any{"key": model.key, "is_reasoning": thinking, "source": "system"}
	req := map[string]any{
		"request_id":       qoderUUID(),
		"request_set_id":   qoderUUID(),
		"chat_record_id":   qoderUUID(),
		"session_id":       qoderUUID(),
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"code_language":    "",
		"chat_prompt":      "",
		"image_urls":       imageURLs,
		"aliyun_user_type": "",
		"system":           "",
		"messages":         wire,
		"tools":            wireTools,
		"parameters":       params,
		"chat_context": map[string]any{
			"chatPrompt": "",
			"imageUrls":  imageURLs,
			"extra": map[string]any{
				"context": []any{},
				"modelConfig": map[string]any{
					"key":          model.key,
					"is_reasoning": thinking,
				},
				"originalContent": lastUser,
			},
			"features": []any{},
			"text":     lastUser,
		},
		"model_config": cfg,
		"business": map[string]any{
			"product":  "cli",
			"version":  "1.0.0",
			"type":     "agent",
			"stage":    "start",
			"id":       qoderUUID(),
			"name":     trimRunes(lastUser, 30),
			"begin_at": time.Now().UnixMilli(),
		},
	}
	return json.Marshal(req)
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func qoderRead(ctx context.Context, r io.Reader, emit func(qoderDelta) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			emit(qoderDelta{done: true})
			return nil
		}
		var env struct {
			StatusCodeValue int    `json:"statusCodeValue"`
			Body            string `json:"body"`
		}
		innerRaw := payload
		if err := json.Unmarshal([]byte(payload), &env); err == nil && (env.Body != "" || env.StatusCodeValue != 0) {
			if env.StatusCodeValue != 0 && env.StatusCodeValue != http.StatusOK {
				emit(qoderDelta{err: fmt.Sprintf("上游状态 %d: %s", env.StatusCodeValue, trimRunes(env.Body, 180))})
				return nil
			}
			if env.Body == "[DONE]" {
				emit(qoderDelta{done: true})
				return nil
			}
			if env.Body == "" {
				continue
			}
			innerRaw = env.Body
		}
		var inner struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(innerRaw), &inner); err != nil {
			continue
		}
		d := qoderDelta{}
		if inner.Usage != nil {
			d.tokensIn = inner.Usage.PromptTokens
			d.tokensOut = inner.Usage.CompletionTokens
		}
		if len(inner.Choices) > 0 {
			delta := inner.Choices[0].Delta
			d.content = delta.Content
			d.reasoning = delta.Reasoning
			for _, tc := range delta.ToolCalls {
				d.tools = append(d.tools, qoderToolDelta{
					index: tc.Index,
					id:    tc.ID,
					name:  tc.Function.Name,
					args:  tc.Function.Arguments,
				})
			}
		}
		if !emit(d) {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	emit(qoderDelta{done: true})
	return nil
}

var qoderStdAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")
var qoderCustomAlphabet = []byte("_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!")

func qoderEncode(plain []byte) []byte {
	std := []byte(base64.StdEncoding.EncodeToString(plain))
	table := make([]byte, 256)
	for i := range table {
		table[i] = byte(i)
	}
	for i := range qoderStdAlphabet {
		table[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
	}
	table['='] = '$'
	n := len(std)
	a := n / 3
	out := make([]byte, n)
	dst := 0
	for i := n - a; i < n; i++ {
		out[dst] = table[std[i]]
		dst++
	}
	for i := a; i < n-a; i++ {
		out[dst] = table[std[i]]
		dst++
	}
	for i := 0; i < a; i++ {
		out[dst] = table[std[i]]
		dst++
	}
	return out
}

const qoderPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

func qoderHeaders(body []byte, requestURL string, creds qoderCreds) (map[string]string, error) {
	keyRaw := strings.ReplaceAll(qoderUUID(), "-", "")
	if len(keyRaw) < 16 {
		keyRaw += "0123456789abcdef"
	}
	aesKey := keyRaw[:16]
	userInfo, _ := json.Marshal(map[string]string{
		"uid":                  creds.UserID,
		"security_oauth_token": creds.Access,
		"name":                 creds.Name,
		"aid":                  "",
		"email":                creds.Email,
	})
	info, err := qoderAES(userInfo, aesKey)
	if err != nil {
		return nil, err
	}
	cosyKey, err := qoderRSA(aesKey)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]string{
		"version":     "v1",
		"requestId":   qoderUUID(),
		"info":        info,
		"cosyVersion": qoderCosyVersion(),
		"ideVersion":  "",
	})
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	sigPath := qoderSigPath(requestURL)
	sum := md5.New()
	_, _ = io.WriteString(sum, payloadB64)
	_, _ = io.WriteString(sum, "\n")
	_, _ = io.WriteString(sum, cosyKey)
	_, _ = io.WriteString(sum, "\n")
	ts := fmt.Sprintf("%d", time.Now().Unix())
	_, _ = io.WriteString(sum, ts)
	_, _ = io.WriteString(sum, "\n")
	_, _ = sum.Write(body)
	_, _ = io.WriteString(sum, "\n")
	_, _ = io.WriteString(sum, sigPath)
	sig := fmt.Sprintf("%x", sum.Sum(nil))
	bodySum := md5.Sum(body)
	machine := creds.MachineID
	if machine == "" {
		machine = qoderUUID()
	}
	return map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKey,
		"Cosy-User":              creds.UserID,
		"Cosy-Date":              ts,
		"Cosy-Version":           qoderCosyVersion(),
		"Cosy-Machineid":         machine,
		"Cosy-Machinetoken":      machine,
		"Cosy-Machinetype":       "5",
		"Cosy-Machineos":         qoderMachineOS(),
		"Cosy-Clienttype":        "5",
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          fmt.Sprintf("%x", bodySum),
		"Cosy-Bodylength":        fmt.Sprintf("%d", len(body)),
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           qoderUUID(),
	}, nil
}

func qoderSigPath(raw string) string {
	u := raw
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[i:]
	} else {
		return "/"
	}
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimPrefix(u, "/algo")
}

func qoderAES(plain []byte, key string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	padded := pkcs7(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(key)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func pkcs7(b []byte, size int) []byte {
	n := size - len(b)%size
	out := append([]byte{}, b...)
	for i := 0; i < n; i++ {
		out = append(out, byte(n))
	}
	return out
}

func qoderRSA(data string) (string, error) {
	block, _ := pem.Decode([]byte(qoderPublicKey))
	if block == nil {
		return "", fmt.Errorf("qoder: 公钥无效")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	k, ok := pub.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("qoder: 公钥类型不对")
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, k, []byte(data))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func qoderCosyVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "1.1.72"
	}
	b, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "qoder-cosy-version-cache.json"))
	if err != nil {
		return "1.1.72"
	}
	var cache struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &cache) != nil || cache.Version == "" {
		return "1.1.72"
	}
	return cache.Version
}

func qoderMachineOS() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64_linux"
	}
	return "x86_64_linux"
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
