// Package plugin 提供跨语言的可执行插件宿主（plugin.json + JSON-Stdio / 纯文本双兼容）。
//
// 设计原则：
//  1. 零语言绑定：任意 Shell / Python / Go / Node 可执行文件都能直接作为插件运行；
//  2. 双层目录发现：工作区 `.mad/plugins/<name>` 优先于用户全局 `~/.multi-agent-desktop/plugins/<name>`；
//  3. 双能力挂载：同一个插件既可向 CLI 注册自定义 `/命令`（output 或 prompt 模式），
//     也可向主对话 Agent 注册标准 `tool.Tool`（受内置审批中心与超时控制保护）。
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"multi-agent-desktop/internal/tool"
)

const defaultPluginTimeout = 30 * time.Second

type CommandSpec struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
	Hint string `json:"hint,omitempty"`
	Run  string `json:"run"`
	// Mode: "output" (默认，直接展示结果) | "prompt" (把生成的 prompt 交给主对话继续推理)
	Mode string `json:"mode,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Run         string          `json:"run"`
	// Writes 默认为 nil -> 按 true 对待以走安全审批；显式声明 false 表示纯只读工具。
	Writes *bool `json:"writes,omitempty"`
}

type Manifest struct {
	Name        string        `json:"name"`
	Version     string        `json:"version,omitempty"`
	Description string        `json:"description,omitempty"`
	TimeoutSec  int           `json:"timeoutSec,omitempty"`
	Commands    []CommandSpec `json:"commands,omitempty"`
	Tools       []ToolSpec    `json:"tools,omitempty"`
}

type LoadedPlugin struct {
	Manifest Manifest
	Dir      string
	Scope    string // "workspace" | "global"
}

type BoundCommand struct {
	PluginName string
	Dir        string
	Timeout    time.Duration
	Spec       CommandSpec
}

type CommandOutput struct {
	Output string `json:"output,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Error  string `json:"error,omitempty"`
}

type stdioRequest struct {
	Kind      string          `json:"kind"` // "command" | "tool"
	Plugin    string          `json:"plugin"`
	Name      string          `json:"name"`
	RawArgs   string          `json:"rawArgs,omitempty"`
	Cwd       string          `json:"cwd"`
	AgentID   string          `json:"agentId,omitempty"`
	ToolInput json.RawMessage `json:"toolInput,omitempty"`
}

type Manager struct {
	mu      sync.RWMutex
	plugins []LoadedPlugin
	cmds    map[string]BoundCommand
}

func NewManager() *Manager {
	return &Manager{
		cmds: make(map[string]BoundCommand),
	}
}

func DefaultGlobalDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".multi-agent-desktop", "plugins")
}

func WorkspacePluginDir(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return filepath.Join(cwd, ".mad", "plugins")
}

func (m *Manager) Load(globalDir, workspaceDir string) error {
	byName := make(map[string]LoadedPlugin)
	var firstErr error

	scan := func(root, scope string) {
		if strings.TrimSpace(root) == "" {
			return
		}
		ents, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			pDir := filepath.Join(root, e.Name())
			mfPath := filepath.Join(pDir, "plugin.json")
			raw, err := os.ReadFile(mfPath)
			if err != nil {
				continue
			}
			var mf Manifest
			if err := json.Unmarshal(raw, &mf); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("解析 %s 失败: %w", mfPath, err)
				}
				continue
			}
			if strings.TrimSpace(mf.Name) == "" {
				mf.Name = e.Name()
			}
			byName[mf.Name] = LoadedPlugin{
				Manifest: mf,
				Dir:      pDir,
				Scope:    scope,
			}
		}
	}

	// 先扫全局，再扫工作区：同名工作区插件覆盖全局插件
	scan(globalDir, "global")
	scan(workspaceDir, "workspace")

	list := make([]LoadedPlugin, 0, len(byName))
	for _, lp := range byName {
		list = append(list, lp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Manifest.Name < list[j].Manifest.Name })

	cmds := make(map[string]BoundCommand)
	for _, lp := range list {
		to := defaultPluginTimeout
		if lp.Manifest.TimeoutSec > 0 {
			to = time.Duration(lp.Manifest.TimeoutSec) * time.Second
		}
		for _, c := range lp.Manifest.Commands {
			clean := normalizeCmdName(c.Name)
			if clean == "" || strings.TrimSpace(c.Run) == "" {
				continue
			}
			c.Name = "/" + clean
			cmds[clean] = BoundCommand{
				PluginName: lp.Manifest.Name,
				Dir:        lp.Dir,
				Timeout:    to,
				Spec:       c,
			}
		}
	}

	m.mu.Lock()
	m.plugins = list
	m.cmds = cmds
	m.mu.Unlock()
	return firstErr
}

func (m *Manager) List() []LoadedPlugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]LoadedPlugin, len(m.plugins))
	copy(out, m.plugins)
	return out
}

func (m *Manager) Commands() []BoundCommand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BoundCommand, 0, len(m.cmds))
	for _, bc := range m.cmds {
		out = append(out, bc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}

func (m *Manager) HasCommand(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.cmds[normalizeCmdName(name)]
	return ok
}

func (m *Manager) ExecCommand(ctx context.Context, name, rawArgs, cwd, agentID string) (CommandOutput, error) {
	key := normalizeCmdName(name)
	m.mu.RLock()
	bc, ok := m.cmds[key]
	m.mu.RUnlock()
	if !ok {
		return CommandOutput{}, fmt.Errorf("未找到插件命令 /%s", key)
	}

	req := stdioRequest{
		Kind:    "command",
		Plugin:  bc.PluginName,
		Name:    key,
		RawArgs: rawArgs,
		Cwd:     cwd,
		AgentID: agentID,
	}
	outBytes, errBytes, err := runPluginProcess(ctx, bc.Dir, bc.Spec.Run, cwd, bc.Timeout, req)
	if err != nil {
		msg := strings.TrimSpace(string(errBytes))
		if msg == "" {
			msg = strings.TrimSpace(string(outBytes))
		}
		if msg != "" {
			return CommandOutput{}, fmt.Errorf("插件 %s 执行失败: %s", bc.PluginName, msg)
		}
		return CommandOutput{}, fmt.Errorf("插件 %s 执行失败: %w", bc.PluginName, err)
	}

	trimmed := bytes.TrimSpace(outBytes)
	var parsed CommandOutput
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, &parsed) == nil {
		if parsed.Error != "" {
			return CommandOutput{}, errors.New(parsed.Error)
		}
		if strings.EqualFold(bc.Spec.Mode, "prompt") && parsed.Prompt == "" && parsed.Output != "" {
			parsed.Prompt = parsed.Output
		}
		return parsed, nil
	}

	text := strings.TrimRight(string(outBytes), "\r\n")
	if strings.EqualFold(bc.Spec.Mode, "prompt") {
		return CommandOutput{Prompt: text}, nil
	}
	return CommandOutput{Output: text}, nil
}

func (m *Manager) Tools(cwd, agentID string) []tool.Tool {
	m.mu.RLock()
	plugins := make([]LoadedPlugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.mu.RUnlock()

	var out []tool.Tool
	for _, lp := range plugins {
		to := defaultPluginTimeout
		if lp.Manifest.TimeoutSec > 0 {
			to = time.Duration(lp.Manifest.TimeoutSec) * time.Second
		}
		for _, ts := range lp.Manifest.Tools {
			if strings.TrimSpace(ts.Name) == "" || strings.TrimSpace(ts.Run) == "" {
				continue
			}
			out = append(out, &bridgedTool{
				pluginName: lp.Manifest.Name,
				pluginDir:  lp.Dir,
				timeout:    to,
				spec:       ts,
				cwd:        cwd,
				agentID:    agentID,
			})
		}
	}
	return out
}

type bridgedTool struct {
	pluginName string
	pluginDir  string
	timeout    time.Duration
	spec       ToolSpec
	cwd        string
	agentID    string
}

func (bt *bridgedTool) Name() string { return bt.spec.Name }

func (bt *bridgedTool) Description() string {
	desc := strings.TrimSpace(bt.spec.Description)
	if desc == "" {
		desc = "Plugin tool from " + bt.pluginName
	}
	return fmt.Sprintf("[Plugin:%s] %s", bt.pluginName, desc)
}

func (bt *bridgedTool) Schema() json.RawMessage {
	if len(bytes.TrimSpace(bt.spec.Parameters)) > 0 {
		return bt.spec.Parameters
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (bt *bridgedTool) Writes() bool {
	if bt.spec.Writes != nil {
		return *bt.spec.Writes
	}
	// 未显式声明只读的外部插件工具默认视为有副作用，纳入审批保护
	return true
}

func (bt *bridgedTool) Preview(_ context.Context, in json.RawMessage) (tool.Preview, error) {
	return tool.Preview{
		Title:  fmt.Sprintf("插件 %s · %s", bt.pluginName, bt.spec.Name),
		Detail: string(in),
	}, nil
}

func (bt *bridgedTool) Execute(ctx context.Context, in json.RawMessage) tool.Result {
	req := stdioRequest{
		Kind:      "tool",
		Plugin:    bt.pluginName,
		Name:      bt.spec.Name,
		Cwd:       bt.cwd,
		AgentID:   bt.agentID,
		ToolInput: in,
	}
	outBytes, errBytes, err := runPluginProcess(ctx, bt.pluginDir, bt.spec.Run, bt.cwd, bt.timeout, req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return tool.Result{Status: tool.StatusTimeout, Content: "plugin execution timed out"}
		}
		errMsg := strings.TrimSpace(string(errBytes))
		if errMsg == "" {
			errMsg = err.Error()
		}
		return tool.Fail("plugin %s/%s failed: %s", bt.pluginName, bt.spec.Name, errMsg)
	}

	trimmed := bytes.TrimSpace(outBytes)
	var parsed struct {
		Content string `json:"content"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, &parsed) == nil {
		if parsed.Error != "" {
			return tool.Fail("%s", parsed.Error)
		}
		if parsed.Content != "" {
			return tool.OK(parsed.Content)
		}
		if parsed.Output != "" {
			return tool.OK(parsed.Output)
		}
	}
	return tool.OK(strings.TrimRight(string(outBytes), "\r\n"))
}

func runPluginProcess(parentCtx context.Context, pluginDir, runTarget, cwd string, timeout time.Duration, req stdioRequest) ([]byte, []byte, error) {
	if timeout <= 0 {
		timeout = defaultPluginTimeout
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}

	fields := strings.Fields(runTarget)
	if len(fields) == 0 {
		return nil, nil, errors.New("empty run command")
	}
	bin := fields[0]
	if strings.HasPrefix(bin, "./") || strings.HasPrefix(bin, "../") || !strings.Contains(bin, string(filepath.Separator)) {
		cand := filepath.Join(pluginDir, bin)
		if _, stErr := os.Stat(cand); stErr == nil {
			bin = cand
		}
	}

	cmd := exec.CommandContext(ctx, bin, fields[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	} else {
		cmd.Dir = pluginDir
	}
	cmd.Env = append(os.Environ(),
		"MAD_PLUGIN_DIR="+pluginDir,
		"MAD_WORKSPACE_DIR="+cwd,
		"MAD_AGENT_ID="+req.AgentID,
		"MAD_RAW_ARGS="+req.RawArgs,
	)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func normalizeCmdName(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "/"))
	return strings.ToLower(s)
}
