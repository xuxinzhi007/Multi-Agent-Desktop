package llm

import "encoding/json"

// Usage 记录一次模型调用里 provider 真实返回的 token 用量。
//
// 铁律：这里只放上游响应里白纸黑字给的数字。拿不到的字段保持 0，
// 绝不用 EstTokens、也不用 prompt+completion 反推把缺口补成事实。
// 上游根本没给 usage 块时，Unknown 置 true，调用方据此区分
// 「用量为 0」和「用量未知」。
type Usage struct {
	// PromptTokens 是输入（prompt）token 数。
	PromptTokens int `json:"promptTokens,omitempty"`
	// CompletionTokens 是输出（completion）token 数。
	CompletionTokens int `json:"completionTokens,omitempty"`
	// TotalTokens 是上游直接给的合计 token 数；上游没给就是 0，不自己加。
	TotalTokens int `json:"totalTokens,omitempty"`
	// CachedReadTokens 是命中缓存的输入 token 数（OpenAI 的
	// prompt_tokens_details.cached_tokens / Anthropic 的
	// cache_read_input_tokens）。
	CachedReadTokens int `json:"cachedReadTokens,omitempty"`
	// CacheWriteTokens 是写入缓存的 token 数（Anthropic 的
	// cache_creation_input_tokens）；OpenAI 兼容形状一般没有这个键。
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
	// Source 标明这份数字来自哪种线上协议形状：
	// "openai.usage"（OpenAI 兼容）、"anthropic.usage"（Messages 原生）、
	// "unknown"（上游没给可解析的 usage）。
	Source string `json:"source,omitempty"`
	// Unknown 表示上游响应里没有 usage 块，上面所有数字都不可信。
	Unknown bool `json:"unknown,omitempty"`
}

// usageWire 同时兼容两种真实线上形状，按出现的键识别是哪一种：
//
// OpenAI / OpenAI 兼容（OpenAI、DeepSeek、Ark 豆包、Ollama、llama.cpp、
// Qoder 网关）：
//
//	"usage": {
//	  "prompt_tokens": 12,
//	  "completion_tokens": 34,
//	  "total_tokens": 46,
//	  "prompt_tokens_details": {"cached_tokens": 5}
//	}
//
// Anthropic Messages 原生：
//
//	"usage": {
//	  "input_tokens": 12,
//	  "output_tokens": 34,
//	  "cache_read_input_tokens": 5,
//	  "cache_creation_input_tokens": 2
//	}
type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`

	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens"`
	CacheWrite   int `json:"cache_creation_input_tokens"`
}

// parseUsage 从上游 usage JSON 解码。raw 为空、null 或不是对象时返回
// Unknown 用量；只搬运真实数字，不做任何估算或合计。
func parseUsage(raw json.RawMessage) Usage {
	if len(raw) == 0 {
		return unknownUsage()
	}
	var w usageWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return unknownUsage()
	}

	// OpenAI 兼容形状：任一 OpenAI 键出现就按这套解析。
	if w.PromptTokens != 0 || w.CompletionTokens != 0 || w.TotalTokens != 0 || w.PromptDetails != nil {
		u := Usage{
			PromptTokens:     w.PromptTokens,
			CompletionTokens: w.CompletionTokens,
			TotalTokens:      w.TotalTokens,
			Source:           "openai.usage",
		}
		if w.PromptDetails != nil {
			u.CachedReadTokens = w.PromptDetails.CachedTokens
		}
		return u
	}

	// Anthropic Messages 原生形状。
	if w.InputTokens != 0 || w.OutputTokens != 0 || w.CacheRead != 0 || w.CacheWrite != 0 {
		return Usage{
			PromptTokens:     w.InputTokens,
			CompletionTokens: w.OutputTokens,
			CachedReadTokens: w.CacheRead,
			CacheWriteTokens: w.CacheWrite,
			Source:           "anthropic.usage",
		}
	}

	return unknownUsage()
}

// unknownUsage 是「上游没给 usage」的标准占位：全部数字为 0，Unknown 置位。
func unknownUsage() Usage {
	return Usage{Source: "unknown", Unknown: true}
}
