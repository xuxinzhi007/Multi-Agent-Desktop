package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/mattn/go-isatty"

	"multi-agent-desktop/internal/agent"
	"multi-agent-desktop/internal/approval"
	gwhost "multi-agent-desktop/internal/gateway/host"
	"multi-agent-desktop/internal/llm"
)

var cliIO atomic.Pointer[headlessIO]

var newDesktopWindow = openDesktopWindow

func continueDesktop(headless bool, api *API) *mygo.Window {
	if headless {
		return nil
	}
	return newDesktopWindow(api)
}

// cliArgs 是 CLI 启动参数。
//
// 目录语义按终端工具惯例：**在哪个目录启动就以哪个目录为准**，只有显式
// -C/--cd（或第一段位置参数）才切。这样 `cd ~/proj && app -headless` 就是
// 最常规的用法，不需要先"新建会话再选目录"。
type cliArgs struct {
	Headless bool
	Help     bool
	AgentID  string
	Dir      string
	Prompt   string
	Resume   bool
	// Model 支持三种写法：序号(1)、provider 名(deepseek)、provider/model。
	Model string
}

func parseCLIArgs(args []string) cliArgs {
	var c cliArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-headless", "--headless":
			c.Headless = true
		case "-agent", "--agent":
			if i+1 < len(args) {
				i++
				c.AgentID = args[i]
			}
		case "-C", "--cd", "--dir":
			if i+1 < len(args) {
				i++
				c.Dir = args[i]
			}
		case "-p", "--prompt":
			if i+1 < len(args) {
				i++
				c.Prompt = args[i]
			}
		case "-m", "--model":
			if i+1 < len(args) {
				i++
				c.Model = args[i]
			}
		case "-resume", "--resume":
			c.Resume = true
		case "-h", "--help":
			c.Help = true
		default:
			// 位置参数当目录：`app -headless ~/proj` 与 `-C ~/proj` 等价。
			if !strings.HasPrefix(args[i], "-") && c.Dir == "" {
				c.Dir = args[i]
			}
		}
	}
	return c
}

// workDir 把 -C / 位置参数 / 当前进程目录收敛成一个绝对路径。
func (c cliArgs) workDir() string {
	if c.Dir != "" {
		if abs, err := filepath.Abs(c.Dir); err == nil {
			return abs
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

type headlessIO struct {
	out          io.Writer
	errOut       io.Writer
	in           *bufio.Reader
	center       *approval.Center
	send         func(event string, data any)
	mu           sync.Mutex
	sawDelta     bool
	sawReasoning bool
}

func (h *headlessIO) write(event string, data any) {
	if h == nil {
		return
	}
	if h.send != nil {
		h.send(event, data)
		return
	}
	switch event {
	case "agent.reasoning":
		if v, ok := toAgentReasoning(data); ok && v.Reasoning != "" {
			h.mu.Lock()
			if strings.HasPrefix(v.Reasoning, "▸ ") {
				if h.errOut != nil {
					fmt.Fprintf(h.errOut, "%s", v.Reasoning)
				}
			} else if !h.sawReasoning {
				h.sawReasoning = true
				if h.errOut != nil {
					fmt.Fprintf(h.errOut, "▸ 思考中…\n")
				}
			}
			h.mu.Unlock()
		}
	case "agent.tool":
		if v, ok := toAgentTool(data); ok {
			h.mu.Lock()
			if h.errOut != nil {
				if v.Phase == "start" {
					label := strings.TrimSpace(v.Content)
					if label == "" {
						label = v.Tool
					}
					fmt.Fprintf(h.errOut, "▸ %s\n", label)
				} else if v.Status == "error" || v.Status == "timeout" {
					fmt.Fprintf(h.errOut, "✗ %s (%s)\n", v.Tool, v.Status)
				} else if v.Diff != "" {
					// 没有界面的模式下，这是唯一能看到「到底改了什么」的地方。
					// 走 stderr：stdout 只留模型的回答，管道下游不会被改动明细污染。
					raw, _ := data.(map[string]any)
					via, _ := raw["via"].(string)
					fmt.Fprint(h.errOut, plainDiffBlock(v.Path, via, v.Diff, plainDiffMaxLines))
				}
			}
			h.mu.Unlock()
		}
	case "agent.delta":
		if v, ok := toAgentDelta(data); ok && v.Delta != "" {
			h.mu.Lock()
			h.sawDelta = true
			if h.out != nil {
				fmt.Fprintf(h.out, "%s", v.Delta)
			}
			h.mu.Unlock()
		}
	case "agent.message":
		if v, ok := toAgentMessage(data); ok && v.Message.Role == "assistant" && v.Message.Content != "" {
			h.mu.Lock()
			already := h.sawDelta
			h.sawDelta = false
			h.sawReasoning = false
			if h.out != nil {
				if already {
					fmt.Fprintf(h.out, "\n")
				} else {
					fmt.Fprintf(h.out, "%s\n", v.Message.Content)
				}
			}
			h.mu.Unlock()
		}
	case "agent.error":
		if v, ok := toAgentError(data); ok && v.Error != "" {
			h.mu.Lock()
			h.sawDelta = false
			h.sawReasoning = false
			target := h.errOut
			if target == nil {
				target = h.out
			}
			if target != nil {
				fmt.Fprintf(target, "\n错误：%s\n", v.Error)
			}
			h.mu.Unlock()
		}
	case "agent.compact":
		// 一轮当中自动压缩了上下文。没有界面的模式下也要说一声，走 stderr。
		if mp, ok := data.(map[string]any); ok {
			if text, _ := mp["text"].(string); text != "" && h.errOut != nil {
				h.mu.Lock()
				fmt.Fprintf(h.errOut, "▸ %s\n", text)
				h.mu.Unlock()
			}
		}
	case "tool.approval":
		h.decide(data)
	}
}

func (h *headlessIO) decide(data any) {
	v, ok := toApprovalEvent(data)
	if !ok || h.center == nil {
		return
	}
	h.mu.Lock()
	// 放行之前要能看到这次到底要做什么：谁发起的、具体内容、要写进去的改动。
	var b strings.Builder
	fmt.Fprintf(&b, "\n审批 %s %s", v.ID, v.Tool)
	if v.AgentName != "" {
		fmt.Fprintf(&b, "  （%s）", v.AgentName)
	}
	fmt.Fprintf(&b, "\n%s\n", v.Title)
	if d := strings.TrimSpace(v.Detail); d != "" && d != strings.TrimSpace(v.Title) {
		fmt.Fprintf(&b, "%s\n", clipLines(d, 12))
	}
	if body, _, _, _ := plainDiffBody(v.Diff, plainDiffMaxLines); body != "" {
		b.WriteString(body)
	}
	b.WriteString("[y/n/always] ")
	fmt.Fprint(h.out, b.String())
	line, err := h.readLineLocked()
	h.mu.Unlock()
	if err != nil {
		_ = h.center.Decide(v.ID, false, false, "终端读取失败")
		return
	}
	allow, remember := headlessDecision(line)
	reason := ""
	if !allow {
		reason = "终端拒绝"
	}
	_ = h.center.Decide(v.ID, allow, remember, reason)
}

// plainDiffMaxLines 是纯文本模式下一次改动最多打出来的行数。
// 再长就刷屏了；完整改动在文件里，用 git diff 看。
const plainDiffMaxLines = 40

// plainDiffBody 把统一 diff 整理成适合直接打到终端的几行：去掉 `--- a/` `+++ b/` 文件头，
// 最多留 maxLines 行。返回正文、增删行数、没显示的行数。
func plainDiffBody(diff string, maxLines int) (body string, added, removed, hidden int) {
	if strings.TrimSpace(diff) == "" {
		return "", 0, 0, 0
	}
	var b strings.Builder
	shown := 0
	for _, ln := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "--- a/") || strings.HasPrefix(ln, "+++ b/") || ln == "" {
			continue
		}
		switch ln[0] {
		case '+':
			added++
		case '-':
			removed++
		}
		if shown >= maxLines {
			hidden++
			continue
		}
		shown++
		b.WriteString("  ")
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "  …（还有 %d 行没显示）\n", hidden)
	}
	return b.String(), added, removed, hidden
}

// plainDiffBlock 是一次写入的完整输出：一行标题（路径、增删行数、哪个子任务改的）加正文。
func plainDiffBlock(path, via, diff string, maxLines int) string {
	body, added, removed, _ := plainDiffBody(diff, maxLines)
	if body == "" {
		return ""
	}
	if path == "" {
		path = "文件"
	}
	head := fmt.Sprintf("✎ %s (+%d -%d)", path, added, removed)
	if via != "" {
		head += " · 子任务 " + via
	}
	return head + "\n" + body
}

// clipLines 最多留 n 行，多出来的折成一句说明。
func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n…（还有 %d 行）", len(lines)-n)
}

func headlessDecision(line string) (allow, remember bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, false
	case "always", "a":
		return true, true
	default:
		return false, false
	}
}

func (h *headlessIO) printf(format string, args ...any) {
	if h == nil || h.out == nil {
		return
	}
	h.mu.Lock()
	fmt.Fprintf(h.out, format, args...)
	h.mu.Unlock()
}

func (h *headlessIO) readLine() (string, error) {
	if h == nil {
		return "", io.EOF
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readLineLocked()
}

func (h *headlessIO) readLineLocked() (string, error) {
	if h.in == nil {
		return "", io.EOF
	}
	s, err := h.in.ReadString('\n')
	if len(s) == 0 && err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func ensureAgentModelReady(mgr *agent.Manager, a *agent.Agent) {
	if mgr == nil || a == nil {
		return
	}
	rawCur := a.EffectiveLLM(mgr)
	cur := llm.ApplyDefaults(rawCur)
	curReady := cur.Model != "" && providerReadyForCLI(cur)
	if !curReady {
		if best, ok := pickBestCLIProvider(mgr.Providers()); ok {
			_ = mgr.SetAgentLLM(a.ID, best)
		}
	} else if cur.Model != rawCur.Model {
		_ = mgr.SetAgentLLM(a.ID, cur)
	}
}

// ensureCLIAgent 保证 CLI 有一个能跑的会话。
//
// 默认**新建**一个（目录就是 cwd），而不是抓 list[0]——CLI 最怕一启动就接进
// 昨天的上下文。要接旧的显式用 -agent/-resume。
func ensureCLIAgent(mgr *agent.Manager, id, cwd string, resume bool) (string, error) {
	if mgr == nil {
		return "", errors.New("没有会话内核")
	}
	if id != "" {
		a, ok := mgr.Get(id)
		if !ok {
			return "", fmt.Errorf("没有这个会话：%s", id)
		}
		if cur := a.EffectiveWorkDir(mgr); !validDir(cur) && cwd != "" {
			if err := mgr.SetAgentWorkDir(id, cwd); err != nil {
				return "", err
			}
		}
		ensureAgentModelReady(mgr, a)
		return id, nil
	}
	if resume {
		list := mgr.List()
		if len(list) == 0 {
			return "", errors.New("还没有会话可接")
		}
		var picked *agent.Agent
		for _, cand := range list {
			if cand.Snapshot().ParentID == "" {
				picked = cand
				break
			}
		}
		if picked == nil {
			picked = list[0]
		}
		id = picked.ID
		if cur := picked.EffectiveWorkDir(mgr); !validDir(cur) && cwd != "" {
			if err := mgr.SetAgentWorkDir(id, cwd); err != nil {
				return "", err
			}
		}
		ensureAgentModelReady(mgr, picked)
		return id, nil
	}

	leadName := "Main"
	if cwd != "" {
		if base := filepath.Base(cwd); base != "" && base != "." && base != string(filepath.Separator) {
			leadName = base
		}
	}
	a := mgr.CreateInheriting(leadName, "主对话", "")
	ensureAgentModelReady(mgr, a)
	if cwd != "" {
		if err := mgr.SetAgentWorkDir(a.ID, cwd); err != nil {
			return "", err
		}
	}
	return a.ID, nil
}

func probeLocalEndpoint(baseURL string, timeout time.Duration) bool {
	if strings.TrimSpace(baseURL) == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func providerDefaultModel(p llm.Config) llm.Config {
	p = llm.ApplyDefaults(p)
	if p.Model != "" {
		return p
	}
	switch p.Provider {
	case "deepseek":
		p.Model = "deepseek-chat"
	case "openai":
		p.Model = "gpt-4o"
	case "claude":
		p.Model = "claude-sonnet-4-5"
	}
	return p
}

func providerReadyForCLI(p llm.Config) bool {
	p = providerDefaultModel(p)
	if p.Provider == "" || p.Provider == "mock" || p.Model == "" {
		return false
	}
	if p.Provider == "llamacpp" || p.Provider == "ollama" {
		return probeLocalEndpoint(p.BaseURL, 80*time.Millisecond)
	}
	if p.Provider == llm.DoubaoCLIProvider {
		return true
	}
	if p.Provider == "qoder" && p.APIKey == "" {
		if saved, ok := llm.LocalQoder(); ok && saved.APIKey != "" {
			return true
		}
	}
	return p.APIKey != ""
}

func pickBestCLIProvider(providers []llm.Config) (llm.Config, bool) {
	// 优先选择带默认/明确模型且连通就绪的 Provider（qoder 默认排在空模型云端之前）
	for _, p := range providers {
		cand := llm.ApplyDefaults(p)
		if cand.Model != "" && providerReadyForCLI(cand) {
			return cand, true
		}
	}
	for _, p := range providers {
		cand := providerDefaultModel(p)
		if providerReadyForCLI(cand) {
			return cand, true
		}
	}
	return llm.Config{}, false
}

func validDir(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func headlessTerminal(h *headlessIO) bool {
	if h == nil || h.out == nil {
		return false
	}
	f, ok := h.out.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func waitCLIMCP(ctx context.Context, mgr *agent.Manager) {
	if mgr == nil || mgr.MCP() == nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	mgr.MCP().WaitReady(wctx)
}

func runHeadless(ctx context.Context, h *headlessIO, mgr *agent.Manager, args cliArgs) error {
	cwd := args.workDir()
	if mgr != nil && cwd != "" {
		// 全局工作目录跟着启动目录走：子会话、审批根、shell cwd 都从这里派生。
		mgr.SetWorkDir(cwd)
	}

	// -p：一次性提问，打印结果就退出，不进 TUI（对齐 claude -p / codex exec）。
	if strings.TrimSpace(args.Prompt) != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := ensureCLIAgent(mgr, args.AgentID, cwd, args.Resume)
		if err != nil {
			return err
		}
		if err := applyCLIModel(mgr, id, args.Model); err != nil {
			return err
		}
		waitCLIMCP(ctx, mgr)
		_, err = mgr.RunPrompt(ctx, id, args.Prompt)
		return err
	}

	if headlessTerminal(h) {
		return runTermUI(ctx, h, mgr, args.AgentID, cwd, args.Resume, args.Model)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := ensureCLIAgent(mgr, args.AgentID, cwd, args.Resume)
	if err != nil {
		return err
	}
	if err := applyCLIModel(mgr, id, args.Model); err != nil {
		return err
	}
	waitCLIMCP(ctx, mgr)
	out := io.Writer(io.Discard)
	if h != nil && h.out != nil {
		out = h.out
	}
	fmt.Fprintf(out, "会话 %s（%s）\n", id, shortPath(cwd))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(out, "> ")
		line, err := h.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/" || line == "/help" {
			fmt.Fprintln(out, slashHelp(cliSlashCommands()))
			continue
		}
		if line == "/quit" || line == "/exit" {
			return nil
		}
		if _, err := mgr.RunPrompt(ctx, id, line); err != nil {
			fmt.Fprintf(out, "错误：%s\n", err.Error())
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

var (
	shutdownTrace   *[]string
	shutdownTraceMu sync.Mutex
)

func noteShutdown(step string) {
	shutdownTraceMu.Lock()
	defer shutdownTraceMu.Unlock()
	if shutdownTrace == nil {
		return
	}
	*shutdownTrace = append(*shutdownTrace, step)
}

func shutdownRuntime(api *API, mgr *agent.Manager, gw *gwhost.Host) {
	if api != nil && api.bot != nil {
		api.bot.Sync(nil)
	}
	if planSvc != nil {
		planSvc.Stop()
	}
	if memBridge != nil {
		memBridge.Close()
	}
	if memStore != nil {
		_ = memStore.Close()
	}
	noteShutdown("gateway")
	if gw != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = gw.Close(closeCtx)
		cancel()
	}
	noteShutdown("manager")
	if mgr != nil {
		if mcps := mgr.MCP(); mcps != nil {
			mcps.Close()
		}
		mgr.Shutdown()
	}
}

const cliUsage = `用法：
  app -headless [目录]              在目录里开会话（默认当前目录）
  app -headless -p "问题"            只问一次，打完结果退出
  app -headless -resume             接着最近的会话
  app -headless -agent <id>         指定会话
  app -headless -m <模型>            序号 / provider / provider/model
  app -headless -C <目录>            切目录（与位置参数等价）
  app -headless -h                  看这段
`

// applyCLIModel 把 -m 的写法落到会话上。语义与 TUI 的 /model 一致，
// 免得两个入口各认一套写法。
func applyCLIModel(mgr *agent.Manager, id, want string) error {
	want = strings.TrimSpace(want)
	if want == "" || mgr == nil || id == "" {
		return nil
	}
	list := mgr.Providers()
	// 支持直接传 provider/model（例如 qoder/Ultimate 或 deepseek/deepseek-reasoner）
	if slash := strings.IndexByte(want, '/'); slash > 0 {
		prov := strings.TrimSpace(want[:slash])
		mod := strings.TrimSpace(want[slash+1:])
		if prov != "" && mod != "" {
			for _, c := range list {
				if c.Provider == prov {
					cfg := llm.ApplyDefaults(c)
					cfg.Model = mod
					return mgr.SetAgentLLM(id, cfg)
				}
			}
		}
	}

	labels := make([]string, 0, len(list))
	for i, c := range list {
		c = providerDefaultModel(c)
		label := c.Provider
		if c.Model != "" {
			label = c.Provider + "/" + c.Model
		}
		labels = append(labels, fmt.Sprintf("%d:%s", i+1, label))
	}
	for i, c := range list {
		rawLabel := c.Provider
		if c.Model != "" {
			rawLabel = c.Provider + "/" + c.Model
		}
		filled := providerDefaultModel(c)
		filledLabel := filled.Provider
		if filled.Model != "" {
			filledLabel = filled.Provider + "/" + filled.Model
		}
		if want == fmt.Sprint(i+1) || want == c.Provider || want == c.Model || want == rawLabel || want == filledLabel {
			if filled.Model == "" && filled.Provider != llm.DoubaoCLIProvider {
				return fmt.Errorf("%s 这条连接没有填模型，先补一个（-m %s/<模型>）", c.Provider, c.Provider)
			}
			return mgr.SetAgentLLM(id, filled)
		}
	}
	if len(labels) == 0 {
		return fmt.Errorf("没有这个模型：%s（还没保存过任何模型，先去桌面端加一个）", want)
	}
	return fmt.Errorf("没有这个模型：%s（已保存：%s）", want, strings.Join(labels, "、"))
}
