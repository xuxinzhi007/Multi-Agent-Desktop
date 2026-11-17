package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// validate 校验参数是否符合 JSON Schema，返回人类可读的错误描述。
// 空字符串表示通过。
//
// 只实现工具定义里实际用到的子集：type / properties / required /
// enum / items / minimum / maximum。完整 JSON Schema 规范太大，
// 而这里唯一目的是「给模型一句能照着改的错误」，
// 引入第三方校验库反而会返回一堆它看不懂的嵌套路径。
func validate(schema json.RawMessage, input json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return "internal: tool schema is malformed"
	}

	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return "arguments must be valid JSON: " + err.Error()
	}
	return checkValue(s, v, "")
}

func checkValue(schema map[string]any, v any, path string) string {
	if want, ok := schema["type"].(string); ok {
		if msg := checkType(want, v, path); msg != "" {
			return msg
		}
	}

	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("%s must be one of %v, got %v", label(path), enum, v)
		}
	}

	switch typed := v.(type) {
	case map[string]any:
		if props, ok := schema["properties"].(map[string]any); ok {
			// 未知字段先查。模型编造参数名（"cmd" 而不是 "command"）
			// 比漏字段更常见，先说「你写错了字段名」比说「缺字段」有用得多 ——
			// 后者会让它再加一个字段，前者才让它改掉错的。
			//
			// 注意：JSON Schema 规范里 additionalProperties 缺省是 true。
			// 我们反过来默认拒绝，因为这个 schema 唯一的消费者是模型：
			// 静默忽略多余字段会让它长期以为自己调用成功。
			// 要放行多余字段的工具显式写 "additionalProperties": true。
			if extra, ok := schema["additionalProperties"].(bool); !ok || !extra {
				for name := range typed {
					if _, known := props[name]; !known {
						return fmt.Sprintf(
							"unknown field %q; allowed fields: %v",
							join(path, name), sortedKeys(props))
					}
				}
			}
			if req, ok := schema["required"].([]any); ok {
				for _, r := range req {
					name, _ := r.(string)
					if _, present := typed[name]; !present {
						return fmt.Sprintf("missing required field %q", join(path, name))
					}
				}
			}
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if val, present := typed[k]; present {
					sub, _ := props[k].(map[string]any)
					if msg := checkValue(sub, val, join(path, k)); msg != "" {
						return msg
					}
				}
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range typed {
				if msg := checkValue(items, item, fmt.Sprintf("%s[%d]", path, i)); msg != "" {
					return msg
				}
			}
		}
	case float64:
		if min, ok := schema["minimum"].(float64); ok && typed < min {
			return fmt.Sprintf("%s must be >= %v, got %v", label(path), min, typed)
		}
		if max, ok := schema["maximum"].(float64); ok && typed > max {
			return fmt.Sprintf("%s must be <= %v, got %v", label(path), max, typed)
		}
	}
	return ""
}

func checkType(want string, v any, path string) string {
	ok := false
	switch want {
	case "string":
		_, ok = v.(string)
	case "number":
		_, ok = v.(float64)
	case "integer":
		// JSON 不区分 int/float，只要求没有小数部分
		f, isNum := v.(float64)
		ok = isNum && f == float64(int64(f))
	case "boolean":
		_, ok = v.(bool)
	case "array":
		_, ok = v.([]any)
	case "object":
		_, ok = v.(map[string]any)
	case "null":
		ok = v == nil
	default:
		ok = true // 未知类型不拦，留给工具自己判断
	}
	if !ok {
		return fmt.Sprintf("%s must be %s, got %s", label(path), want, jsonType(v))
	}
	return ""
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func label(path string) string {
	if path == "" {
		return "arguments"
	}
	return path
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
