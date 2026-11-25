package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
		"定位页面上的结果时，用 steps 一次做完，不要每点一下就停。每步用 text 写页面上能看见的文字，例如先点「环境」再点「test」，输入用 text 指定输入框、value 写内容。下拉打开后的选项写在下一步，浏览器会自己等到它出现。最多 12 步，只有最后的页面会返回。编号过期就改用 text，不要为了试错连着 snapshot。" +
		"引擎是 Safari/WebKit，不是 Chrome：只支持 Chrome 的页面特性这里可能没有，遇到就换路径。open 后若被重定向（如跳登录页），返回的 url 就是实际地址，先处理登录再重试。"
}

// Writes 返回 false：web 只操作应用内置浏览器，不碰工作目录里的任何文件。
//
// 之前标为 true，每次 open 一张网页都要挂一张审批卡，实际把「查资料」变成了
// 「先等用户点允许」，model 等不到就以为环境没有联网能力。
// 内置浏览器是独立沙箱（不是用户的 Chrome），改页面不产生本地副作用；
// 真正需要审批的是 bash 与文件写入，它们在那些工具里各自拦。
func (*Tool) Writes() bool { return false }

func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"op": {"type": "string", "enum": ["snapshot", "read", "open", "back", "forward", "reload", "move", "click", "double_click", "right_click", "down", "up", "scroll", "type", "key", "press", "wait"]},
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
			"timeoutMs": {"type": "integer"},
			"steps": {
				"type": "array",
				"maxItems": 12,
				"items": {
					"type": "object",
					"properties": {
						"op": {"type": "string"},
						"url": {"type": "string"},
						"ref": {"type": "string"},
						"text": {"type": "string"},
						"value": {"type": "string"},
						"key": {"type": "string"},
						"mods": {"type": "array", "items": {"type": "string", "enum": ["ctrl", "shift", "alt", "cmd", "meta"]}},
						"dx": {"type": "number"},
						"dy": {"type": "number"},
						"urlContains": {"type": "string"},
						"timeoutMs": {"type": "integer"},
						"x": {"type": "number"},
						"y": {"type": "number"}
					},
					"required": ["op"]
				}
			}
		},
		"additionalProperties": false
	}`)
}

func stepsOf(raw json.RawMessage) ([]browser.Op, error) {
	var wrap struct {
		browser.Op
		Steps []browser.Op `json:"steps"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	if len(wrap.Steps) == 0 {
		if strings.TrimSpace(wrap.Op.Op) == "" {
			return nil, errors.New("给 op（单步）或 steps（多步），例如 {\"op\":\"snapshot\"}")
		}
		return []browser.Op{wrap.Op}, nil
	}
	if len(wrap.Steps) > 12 {
		wrap.Steps = wrap.Steps[:12]
	}
	// 顶层又写了 op 时以 steps 为准：那一句通常是模型把单步写法和多步写法抄在了一起，
	// 接在前面会把同一个动作做两次。
	return wrap.Steps, nil
}

func run(ctx context.Context, ops []browser.Op) (browser.Shot, error) {
	d := browser.CurrentDesk()
	if d == nil {
		return browser.Shot{}, errors.New("内置浏览器还没打开")
	}
	return d.Run(ctx, ops)
}

func (*Tool) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	ops, err := stepsOf(raw)
	if err != nil {
		return tool.Fail("参数无法解析：%v", err)
	}
	shot, err := run(ctx, ops)
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
