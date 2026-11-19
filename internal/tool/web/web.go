package web

import (
	"context"
	"encoding/json"

	"multi-agent-desktop/internal/browser"
	"multi-agent-desktop/internal/tool"
)

type Tool struct{}

func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "web" }

func (*Tool) Description() string {
	return "操作用户已经打开的浏览器页面。op 取 read、open、click、type、press。" +
		"read 读取当前标签的网址和正文；open 把当前标签打开到 http(s) 链接；" +
		"click 的 text 必须是页面上看得见的文字；type 往输入框写 value，可用 text 指定框；" +
		"press 只按 Enter、Escape 或 Tab。app 填浏览器名字，例如 Google Chrome 或 Safari。" +
		"只动这一页，不要编造脚本。"
}

func (*Tool) Writes() bool { return true }

func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"op": {"type": "string", "enum": ["read", "open", "click", "type", "press"]},
			"app": {"type": "string"},
			"url": {"type": "string"},
			"text": {"type": "string"},
			"value": {"type": "string"},
			"key": {"type": "string"}
		},
		"required": ["op"],
		"additionalProperties": false
	}`)
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var op browser.Op
	if err := json.Unmarshal(raw, &op); err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	out, err := browser.Act(ctx, op)
	if err != nil {
		return tool.Fail("%s", err.Error())
	}
	if out == "" {
		out = "ok"
	}
	return tool.OK(out)
}
