package acp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rpcMessage 是 JSON-RPC 2.0 的统一帧。请求、响应、通知共用一个结构，
// 用字段是否为空来区分类型。
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("acp rpc error %d: %s", e.Code, e.Message)
}

// idKey 把请求 id 归一化成字符串，用来查 pending 表。
// 规范允许 string 或 number，对端可能两种都用；我们不假设。
func idKey(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return out
		}
	}
	return s
}

func rawID(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// 常用 JSON-RPC 错误码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)
