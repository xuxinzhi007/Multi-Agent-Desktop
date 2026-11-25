package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileBinder 把绑定关系持久化成一个 JSON 文件：<dataDir>/gateway-bindings.json。
// 写盘走「同目录临时文件 + rename」，崩溃或并发写不会留下半个 JSON。
type FileBinder struct {
	mu    sync.Mutex
	path  string
	items map[string]Binding
	order []string
}

// bindingKey 是绑定在内存里的键。channel 与 chatID 之间用不会出现在 ID 里的分隔符。
func bindingKey(channel, externalChatID string) string {
	return strings.TrimSpace(channel) + "\x00" + strings.TrimSpace(externalChatID)
}

// NewFileBinder 打开（或新建）绑定文件。数据目录不存在时会创建。
func NewFileBinder(dataDir string) (*FileBinder, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("数据目录为空")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录：%w", err)
	}
	b := &FileBinder{
		path:  filepath.Join(dataDir, "gateway-bindings.json"),
		items: map[string]Binding{},
	}
	if err := b.load(); err != nil {
		return nil, err
	}
	return b, nil
}

type binderFile struct {
	Bindings []Binding `json:"bindings"`
}

func (b *FileBinder) load() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取绑定文件：%w", err)
	}
	var f binderFile
	if err := json.Unmarshal(buf, &f); err != nil {
		return fmt.Errorf("解析绑定文件 %s：%w", b.path, err)
	}
	b.items = map[string]Binding{}
	b.order = nil
	for _, item := range f.Bindings {
		item := normalizeBinding(item)
		key := bindingKey(item.Channel, item.ExternalChatID)
		b.items[key] = item
		b.order = append(b.order, key)
	}
	return nil
}

func normalizeBinding(in Binding) Binding {
	in.Channel = strings.TrimSpace(in.Channel)
	in.ExternalChatID = strings.TrimSpace(in.ExternalChatID)
	in.AgentID = strings.TrimSpace(in.AgentID)
	if in.Policy == "" {
		in.Policy = PolicyAllowAll
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = time.Now().Unix()
	}
	return in
}

func (b *FileBinder) Bind(in Binding) error {
	in = normalizeBinding(in)
	if in.Channel == "" || in.ExternalChatID == "" {
		return errors.New("绑定缺少渠道或会话 ID")
	}
	if in.AgentID == "" {
		return errors.New("绑定缺少 Agent ID")
	}

	b.mu.Lock()
	key := bindingKey(in.Channel, in.ExternalChatID)
	if _, exists := b.items[key]; !exists {
		b.order = append(b.order, key)
	}
	b.items[key] = in
	err := b.saveLocked()
	b.mu.Unlock()
	return err
}

func (b *FileBinder) Resolve(channel, externalChatID string) (Binding, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.items[bindingKey(channel, externalChatID)]
	return item, ok
}

func (b *FileBinder) List() []Binding {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Binding, 0, len(b.order))
	for _, key := range b.order {
		if item, ok := b.items[key]; ok {
			out = append(out, item)
		}
	}
	// 稳定顺序：渠道 + 会话 ID，避免落盘/重启后列表抖动。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].ExternalChatID < out[j].ExternalChatID
	})
	return out
}

func (b *FileBinder) Unbind(channel, externalChatID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := bindingKey(channel, externalChatID)
	if _, ok := b.items[key]; !ok {
		return nil
	}
	delete(b.items, key)
	for i, cur := range b.order {
		if cur == key {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	return b.saveLocked()
}

// saveLocked 写临时文件再 rename。调用方必须已持有 b.mu。
func (b *FileBinder) saveLocked() error {
	f := binderFile{Bindings: make([]Binding, 0, len(b.order))}
	for _, key := range b.order {
		if item, ok := b.items[key]; ok {
			f.Bindings = append(f.Bindings, item)
		}
	}
	buf, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化绑定：%w", err)
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("写绑定临时文件：%w", err)
	}
	if err := os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换绑定文件：%w", err)
	}
	return nil
}
