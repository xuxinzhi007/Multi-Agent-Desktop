package web

import (
	"context"
	"encoding/json"
	"errors"

	"multi-agent-desktop/internal/browser"
	"multi-agent-desktop/internal/tool"
)

type Tool struct{}

func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "web" }

func (*Tool) Description() string {
	return "操作应用内置浏览器。op 取 read、open、click、type、press。" +
		"窗口会打开在应用自己的浏览器里，登录留在那里，不使用用户的 Chrome。" +
		"read 读取当前页的网址、标题和正文，会等客户端渲染完成，Flutter 页面读的是无障碍树里的文字；open 打开 http(s) 链接；" +
		"click 的 text 必须是页面上看得见的文字；type 往输入框写 value，可用 text 指定框；" +
		"press 只按 Enter、Escape 或 Tab。不要编造脚本。"
}

func (*Tool) Writes() bool { return true }

func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"op": {"type": "string", "enum": ["read", "open", "click", "type", "press"]},
			"url": {"type": "string"},
			"text": {"type": "string"},
			"value": {"type": "string"},
			"key": {"type": "string"}
		},
		"required": ["op"],
		"additionalProperties": false
	}`)
}

func act(ctx context.Context, op browser.Op) (string, error) {
	d := browser.CurrentDesk()
	if d == nil {
		return "", errors.New("内置浏览器还没打开")
	}
	return d.Act(ctx, op)
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var op browser.Op
	if err := json.Unmarshal(raw, &op); err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	out, err := act(ctx, op)
	if err != nil {
		return tool.Fail("%s", err.Error())
	}
	if out == "" {
		out = "ok"
	}
	return tool.OK(out)
}
