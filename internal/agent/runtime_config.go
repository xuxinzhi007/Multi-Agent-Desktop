package agent

import (
	"fmt"
	"strings"
)

// ACPConfig 是外部 ACP Agent 的启动配置。Runtime == "acp" 时有效。
type ACPConfig struct {
	Name    string   `json:"name,omitempty"` // 展示名，空则用 command 的 basename
	Command string   `json:"command"`        // 可执行文件；Windows 上允许 .cmd/.bat
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`   // KEY=VALUE，追加到子进程环境
	Trace   bool     `json:"trace,omitempty"` // 为真时把 ACP 原始帧写到数据目录
}

// 运行时取值。空串和旧的 "loop" 都按 native 处理，老 agents.json 无需迁移。
const (
	RuntimeNative = "native"
	RuntimeACP    = "acp"
)

// normalizeRuntime 把落盘/界面传来的运行时字符串收成合法值。
// 返回空串表示非法（API 层据此报错）。
func normalizeRuntime(raw string) string {
	switch strings.TrimSpace(raw) {
	case "", "native", "loop":
		return RuntimeNative
	case "acp":
		return RuntimeACP
	}
	return ""
}

// EffectiveRuntime 返回这个会话当前生效的运行时，恒为 native 或 acp。
func (a *Agent) EffectiveRuntime() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if normalizeRuntime(a.Runtime) == RuntimeACP {
		return RuntimeACP
	}
	return RuntimeNative
}

// SetRuntime 设置会话运行时与 ACP 启动配置。
// 切回 native 时保留已有 ACP 配置（用户可能切回来）；acp 配置由调用方校验过命令非空。
func (m *Manager) SetRuntime(id, runtime string, acp *ACPConfig) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	norm := normalizeRuntime(runtime)
	if norm == "" {
		return fmt.Errorf("运行时只能是 native 或 acp")
	}
	a.mu.Lock()
	a.Runtime = norm
	if acp != nil {
		cp := *acp
		cp.Args = cleanEnvList(cp.Args)
		cp.Env = cleanEnvList(cp.Env)
		a.ACP = &cp
	}
	a.mu.Unlock()
	m.goSave()
	return nil
}

// setRuntimeState 写运行期连接状态并广播一次 agent.config。
// 只在状态切换时调用，不在每个 delta 上重复发；errText 非空即失败原因。
func (a *Agent) setRuntimeState(m *Manager, state, errText string) {
	a.mu.Lock()
	a.runtimeState = state
	a.runtimeError = errText
	a.mu.Unlock()
	m.emit("agent.config", a.viewMap())
}

// RuntimeState / RuntimeError 是只读访问器，供主程序出视图使用。
func (a *Agent) RuntimeState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runtimeState
}

func (a *Agent) RuntimeError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runtimeError
}

// cleanEnvList 去掉空白项，避免把空参数/空环境变量带进子进程。
func cleanEnvList(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Copy 返回 ACP 配置的深拷贝，落盘/快照/视图时避免共享底层切片。
func (c *ACPConfig) Copy() *ACPConfig {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Args = append([]string(nil), c.Args...)
	cp.Env = append([]string(nil), c.Env...)
	return &cp
}
