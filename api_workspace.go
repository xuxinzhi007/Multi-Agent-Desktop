package main

// 工作目录、内置浏览器、shell、窗口与系统杂项。

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/feishu"
	"multi-agent-desktop/internal/llm"
	"multi-agent-desktop/internal/privacy"
	"multi-agent-desktop/internal/workdir"
)

// ImageData 按 id 读一张已经落盘的图。
func (s *API) ImageData(id string) (llm.ImageFile, error) {
	return llm.File(s.imageDir, id)
}

func (s *API) blocksFrom(req feishu.RunRequest) ([]llm.Block, string, error) {
	var blocks []llm.Block
	var lines []string
	for _, p := range req.Pieces {
		if len(p.Data) > 0 {
			mime := p.MIME
			if mime == "" {
				mime = "image/png"
			}
			id, err := llm.SaveImage(s.imageDir, mime, p.Data)
			if err != nil {
				return nil, "", err
			}
			blocks = append(blocks, llm.Block{ID: id, MIME: mime, Data: append([]byte(nil), p.Data...)})
			continue
		}
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		blocks = append(blocks, llm.Block{Text: p.Text})
		lines = append(lines, p.Text)
	}
	return blocks, strings.Join(lines, "\n"), nil
}

// RunShell 执行 shell 命令。ctx 取消会杀掉整个进程组。
// 同一条命令还在跑时不再开第二遍。
func (s *API) RunShell(ctx context.Context, req ShellRequest) {
	key := req.AgentID + "\n" + req.Command
	s.shellMu.Lock()
	if s.shellBusy == nil {
		s.shellBusy = map[string]bool{}
	}
	if s.shellBusy[key] {
		s.shellMu.Unlock()
		return
	}
	s.shellBusy[key] = true
	s.shellMu.Unlock()

	dir := ""
	if s.mgr != nil && req.AgentID != "" {
		if a, ok := s.mgr.Get(req.AgentID); ok {
			dir = a.EffectiveWorkDir(s.mgr)
		}
	}
	go func() {
		defer func() {
			s.shellMu.Lock()
			delete(s.shellBusy, key)
			s.shellMu.Unlock()
		}()
		ShellStart.Emit(s.win, ShellEvent{AgentID: req.AgentID, Command: req.Command})
		res := s.runner.RunIn(ctx, dir, req.Command)
		ShellResult_.Emit(s.win, ShellResultEvent{
			AgentID: req.AgentID,
			Command: req.Command,
			Result:  res,
		})
	}()
}

// GetWorkDir 返回工作目录。
func (s *API) GetWorkDir() string {
	return s.runner.Dir()
}

// SetWorkDir 切换全局 shell 工作目录（新建会话的默认值），并落盘。
func (s *API) SetWorkDir(dir string) string {
	if workdir.TooBroad(dir) {
		log.Printf("set workdir: %s", workdir.RejectReason)
		return s.runner.Dir()
	}
	s.runner.SetDir(dir)
	if s.mgr != nil {
		s.mgr.SetWorkDir(dir)
	}
	if s.settings != nil {
		if err := s.settings.SetWorkDir(dir); err != nil {
			log.Printf("persist workdir: %v", err)
		}
	}
	WorkDirEvt.Emit(s.win, WorkDirEvent{Dir: dir})
	return dir
}

// OpenPrivacy 打开系统设置里对应的权限页，方便之后授权。
func (s *API) OpenPrivacy(kind string) error {
	pane := map[string]string{
		"screen":        "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
		"accessibility": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		"automation":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
	}[kind]
	if pane == "" {
		return fmt.Errorf("未知权限")
	}
	return mygo.Shell.OpenExternal(pane)
}

// OpenWorkbench 打开应用自己的浏览器。地址为空时只把窗口带到前面。
func (s *API) OpenWorkbench(url string) (string, error) {
	if s.desk == nil {
		return "", fmt.Errorf("内置浏览器还没准备好")
	}
	return s.desk.Open(url)
}

// PrivacyStatus 读取屏幕录制、辅助功能和自动化是否已经允许。不弹授权框。
func (s *API) PrivacyStatus() []PrivacyGrant {
	st := privacy.Status()
	out := make([]PrivacyGrant, 0, 3)
	for _, kind := range []string{"screen", "accessibility", "automation"} {
		state := st[kind]
		if state != "yes" && state != "no" {
			state = "unknown"
		}
		out = append(out, PrivacyGrant{Kind: kind, State: state})
	}
	return out
}

// SettingsPath 返回配置文件路径。
func (s *API) SettingsPath() string {
	if s.settings == nil {
		return ""
	}
	return s.settings.Path()
}

// SetAlwaysOnTop 控制窗口置顶（悬浮）。
func (s *API) SetAlwaysOnTop(v bool) bool {
	if s.win != nil {
		s.win.SetAlwaysOnTop(v)
	}
	return v
}

// IsAlwaysOnTop 返回窗口是否置顶。
func (s *API) IsAlwaysOnTop() bool {
	if s.win == nil {
		return false
	}
	return s.win.IsAlwaysOnTop()
}

// PickDirectory 弹出原生目录选择对话框。
func (s *API) PickDirectory() string {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:       "选择工作目录",
		Directory:   true,
		ButtonLabel: "选择",
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}
