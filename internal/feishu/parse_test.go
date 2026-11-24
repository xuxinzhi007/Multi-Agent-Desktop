package feishu

import (
	"strings"
	"testing"
)

func TestReadMessageByType(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		body  string
		text  string
		label string
		media int
	}{
		{"文本", "text", `{"text":"你好"}`, "你好", "text", 0},
		{"空文本", "text", `{"text":""}`, "[空消息]", "text", 0},
		{"富文本", "post", `{"title":"周报","content":[[{"tag":"text","text":"进展"}],[{"tag":"a","text":"链接","href":"https://x"}],[{"tag":"img","image_key":"img_1"}]]}`, "周报\n进展\n链接\n[图片]", "post", 1},
		{"图片", "image", `{"image_key":"img_1"}`, "[图片]", "image", 1},
		{"文件", "file", `{"file_key":"file_1","file_name":"报告.pdf"}`, "[文件] 报告.pdf", "file", 1},
		{"语音", "audio", `{"file_key":"file_1","duration":3200}`, "[语音] 4″", "audio", 1},
		{"视频", "media", `{"file_key":"file_1","file_name":"演示.mp4"}`, "[视频] 演示.mp4", "video", 1},
		{"表情", "sticker", `{"file_key":"file_1"}`, "[表情]", "sticker", 0},
		{"卡片旧结构", "interactive", `{"title":null,"elements":[[{"tag":"text","text":"可以换成这些会话"}]],"header":{"title":{"tag":"plain_text","content":"选择会话"}}}`, "选择会话\n可以换成这些会话", "card", 0},
		{"卡片按钮", "interactive", `{"elements":[{"tag":"div","text":{"tag":"lark_md","content":"要不要重启？"}},{"tag":"action","actions":[{"tag":"button","text":{"tag":"plain_text","content":"停止"}}]}]}`, "要不要重启？\n停止", "card", 0},
		{"卡片降级留标题", "interactive", `{"title":"🔄 会话已重置","elements":[[{"tag":"img","image_key":"img_1"},{"tag":"text","text":"请升级至最新版本客户端，以查看内容"},{"tag":"text","text":""}]]}`, "🔄 会话已重置\n[卡片内容需在飞书客户端查看]", "card", 0},
		{"卡片降级无标题", "interactive", `{"title":null,"elements":[[{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`, "[卡片内容需在飞书客户端查看]", "card", 0},
		{"卡片实体", "interactive", `{"type":"card","data":{"card_id":"7355372766134157313"}}`, "[卡片内容需在飞书客户端查看]（card_id: 7355372766134157313）", "card", 0},
		{"卡片2.0", "interactive", `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"构建失败"}},"body":{"elements":[{"tag":"markdown","content":"**原因**：超时"},{"tag":"column_set","columns":[{"tag":"column","elements":[{"tag":"plain_text","content":"耗时 2.3s"}]}]},{"tag":"button","text":{"tag":"plain_text","content":"重试"}}]}}`, "构建失败\n**原因**：超时\n耗时 2.3s\n重试", "card", 0},
		{"富文本降级", "post", `{"content":[[{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`, "[卡片内容需在飞书客户端查看]", "post", 0},
		{"群名片", "share_chat", `{"chat_id":"oc_1"}`, "[群名片]", "share", 0},
		{"个人名片", "share_user", `{"user_id":"ou_1"}`, "[个人名片]", "share", 0},
		{"合并转发", "merge_forward", `{"title":"群聊记录","message_list":[{"sender_name":"张三","message_type":"text","content":"{\"text\":\"在吗\"}"}]}`, "群聊记录\n张三：在吗", "forward", 0},
		{"系统消息", "system", `{"template":"{from_user} 邀请 {to_chatters} 加入群聊"}`, "[系统消息] {from_user} 邀请 {to_chatters} 加入群聊", "system", 0},
		{"不支持", "calendar", `{}`, "[不支持的消息类型: calendar]", "unsupported", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, label, media := readMessage(c.kind, c.body, nil)
			if text != c.text {
				t.Fatalf("text = %q, want %q", text, c.text)
			}
			if label != c.label {
				t.Fatalf("kind = %q, want %q", label, c.label)
			}
			if len(media) != c.media {
				t.Fatalf("media = %d, want %d", len(media), c.media)
			}
		})
	}
}

// 卡片解析不能把整段 JSON 糊到界面上。
func TestCardTextIsReadable(t *testing.T) {
	text := cardText(`{"elements":[[{"tag":"text","text":"第一行"}],[{"tag":"text","text":"第二行"}]]}`)
	if text != "第一行\n第二行" {
		t.Fatalf("cardText = %q", text)
	}
	if strings.ContainsAny(text, "{}[]\"") {
		t.Fatalf("卡片里混进了 JSON 符号：%q", text)
	}
}

func TestReadMessageMentionReplaced(t *testing.T) {
	text, _, _ := readMessage("text", `{"text":"@_user_1 收到"}`, []mention{{Key: "@_user_1", Name: "卢锐勇"}})
	if !strings.Contains(text, "@卢锐勇") {
		t.Fatalf("mention 没替换：%q", text)
	}
}
