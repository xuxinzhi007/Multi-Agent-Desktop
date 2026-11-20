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
	return "操作应用内置浏览器。登录留在这个窗口里，不使用用户的 Chrome。" +
		"先 snapshot 看控件编号，再用编号操作。没有 DOM 的区域用 x、y。" +
		"op 可以是 snapshot、open、back、forward、reload、move、click、double_click、right_click、down、up、scroll、type、key、wait。" +
		"每次动作都会交回网址、标题、正文、控件表和当前画面。编号只对刚刚这次观察有效。不要编造脚本。"
}

func (*Tool) Writes() bool { return true }

func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"op": {"type": "string", "enum": ["snapshot", "read", "open", "back", "forward", "reload", "move", "click", "double_click", "right_click", "down", "up", "scroll", "type", "key", "wait"]},
			"url": {"type": "string"},
			"ref": {"type": "string"},
			"x": {"type": "number"},
			"y": {"type": "number"},
			"text": {"type": "string"},
			"value": {"type": "string"},
			"key": {"type": "string"},
			"mods": {"type": "array", "items": {"type": "string", "enum": ["ctrl", "shift", "alt", "cmd", "meta"]}},
			"dx": {"type": "number"},
			"dy": {"type": "number"},
			"urlContains": {"type": "string"},
			"timeoutMs": {"type": "integer"}
		},
		"required": ["op"],
		"additionalProperties": false
	}`)
}

func act(ctx context.Context, op browser.Op) (browser.Shot, error) {
	d := browser.CurrentDesk()
	if d == nil {
		return browser.Shot{}, errors.New("内置浏览器还没打开")
	}
	return d.Act(ctx, op)
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var op browser.Op
	if err := json.Unmarshal(raw, &op); err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	shot, err := act(ctx, op)
	if err != nil && shot.Body == "" {
		return tool.Fail("%s", err.Error())
	}
	body := shot.Body
	if body == "" {
		body = "ok"
	}
	res := tool.OK(body)
	res.Image = shot.PNG
	if err != nil {
		res.Status = tool.StatusError
		res.Content = err.Error() + "\n" + body
	}
	return res
}
