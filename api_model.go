package main

// 模型与诊断：厂商凭据、模型列表、连接检查、进程信息。

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"multi-agent-desktop/internal/llm"
)

// GetLLMConfig 返回当前模型配置。
func (s *API) GetLLMConfig() LLMConfig {
	return llm.Redacted(s.mgr.LLMConfig())
}

// SetLLMConfig 切换全局模型配置，并落盘到 config.json。
// 落盘失败不阻断本次生效，但要记日志 —— 用户下次启动会回到旧配置。
func (s *API) SetLLMConfig(cfg LLMConfig) LLMConfig {
	cfg = s.keepAPIKey(cfg)
	s.mgr.SetLLMConfig(cfg)
	if s.settings != nil {
		if err := s.settings.SetLLM(cfg); err != nil {
			log.Printf("persist llm config: %v", err)
		}
	}
	if cfg.Provider != "" && cfg.Provider != "mock" {
		s.rememberProvider(cfg)
	}
	pub := llm.Redacted(cfg)
	LLMConfigEvt.Emit(s.win, pub)
	return pub
}

// keepAPIKey 在前端没再带密钥时沿用已保存的。空白表示不修改。
func (s *API) keepAPIKey(cfg LLMConfig) LLMConfig {
	if cfg.APIKey != "" {
		return cfg
	}
	prev := s.mgr.LLMConfig()
	if cfg.Provider == prev.Provider {
		cfg.APIKey = prev.APIKey
	}
	if cfg.APIKey == "" {
		if saved, ok := s.mgr.SavedProvider(cfg.Provider); ok {
			cfg.APIKey = saved.APIKey
		}
	}
	return cfg
}

// ListModels 返回指定配置下可用的模型列表。
func (s *API) ListModels(ctx context.Context, cfg LLMConfig) ([]string, error) {
	// 设置面板用的是「正在编辑的草稿」，所以这里接受传入配置。
	// 但缺 Key / BaseURL 时同样快速失败，不要发无意义请求。
	return s.listModelsWithSavedCreds(ctx, cfg)
}

// ListAllModels 返回一个扁平的可用模型列表。
//
// 存在的理由：原来的设置页要用户先选 provider、再填 BaseURL、再填 Key、
// 再选模型 —— 四步里任何一步不懂就卡住。而用户真正要做的事只有一件：
// 「我现在想用哪个模型」。这里把它压成一次点击。
func (s *API) ListAllModels(ctx context.Context) []ModelChoice {
	base := s.mgr.LLMConfig()
	creds := s.providerCreds()
	var out []ModelChoice

	add := func(provider string) {
		cfg, configured := creds[provider]
		// 豆包 CLI 是本机能力：没保存过也能直接用，只要本机装了 CLI。
		if provider == llm.DoubaoCLIProvider {
			configured = true
			cfg = llm.ApplyDefaults(llm.Config{Provider: provider})
		}
		if !configured {
			cfg = llm.ApplyDefaults(llm.Config{Provider: provider})
		}
		local := llm.IsLocalProvider(provider)
		if !configured {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				Local:    local,
				Ready:    false,
				Note:     "未保存",
			})
			return
		}
		if !local && cfg.APIKey == "" {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				BaseURL:  cfg.BaseURL,
				Ready:    false,
				Note:     "未配置 Key",
			})
			return
		}

		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		models, err := llm.ListModelMetas(listCtx, cfg)
		if err != nil {
			out = append(out, ModelChoice{
				Model:    providerLabel(provider),
				Provider: provider,
				BaseURL:  cfg.BaseURL,
				Local:    local,
				Ready:    false,
				Note:     reasonOf(err),
			})
			return
		}
		for _, m := range models {
			out = append(out, ModelChoice{
				Model:     m.Name,
				Provider:  provider,
				BaseURL:   cfg.BaseURL,
				Local:     local,
				Current:   provider == base.Provider && base.Model == m.Name,
				Ready:     true,
				Reasoning: m.Reasoning,
				Efforts:   m.Efforts,
			})
		}
	}

	for _, id := range knownProviderIDs {
		add(id)
	}
	return out
}

// ListProviderEndpoints 列出全部厂商。已保存的会当场检查连接，并带回它的模型。
func (s *API) ListProviderEndpoints(ctx context.Context) []ProviderEndpoint {
	saved := s.providerCreds()
	out := make([]ProviderEndpoint, len(knownProviderIDs))
	var wg sync.WaitGroup
	for i, id := range knownProviderIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			cfg, ok := saved[id]
			out[i] = s.endpointFor(ctx, id, cfg, ok)
		}(i, id)
	}
	wg.Wait()
	return out
}

// SaveProvider 保存一个厂商的地址和 Key，然后检查它现在是否生效。
func (s *API) SaveProvider(ctx context.Context, cfg LLMConfig) (ProviderEndpoint, error) {
	if !knownProvider(cfg.Provider) {
		return ProviderEndpoint{}, fmt.Errorf("不支持的服务厂商 %s", cfg.Provider)
	}
	cfg = llm.ApplyDefaults(cfg)
	if cfg.Provider == "qoder" && cfg.APIKey != "" {
		adopted, err := llm.AdoptQoderAPIKey(ctx, cfg.APIKey)
		if err != nil {
			return ProviderEndpoint{}, err
		}
		cfg.APIKey = adopted.APIKey
		if cfg.BaseURL == "" {
			cfg.BaseURL = adopted.BaseURL
		}
	}
	if prev, ok := s.mgr.SavedProvider(cfg.Provider); ok {
		if cfg.APIKey == "" {
			cfg.APIKey = prev.APIKey
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = prev.BaseURL
		}
	}
	cfg.Model = ""
	// 豆包 CLI 没有地址和 Key，只有「本机装没装、登没登录」。
	if cfg.Provider == llm.DoubaoCLIProvider {
		ok, text := llm.DoubaoCLIStatus(ctx)
		if !ok {
			return ProviderEndpoint{}, fmt.Errorf("豆包 CLI 不可用：%s", text)
		}
		s.rememberProvider(cfg)
		return s.endpointFor(ctx, cfg.Provider, cfg, true), nil
	}
	if cfg.BaseURL == "" {
		return ProviderEndpoint{}, fmt.Errorf("请先填写服务地址")
	}
	if !llm.IsLocalProvider(cfg.Provider) && cfg.APIKey == "" {
		return ProviderEndpoint{}, fmt.Errorf("请先填写 API Key")
	}
	s.rememberProvider(cfg)
	base := s.mgr.LLMConfig()
	if base.Provider == cfg.Provider {
		base.BaseURL = cfg.BaseURL
		base.APIKey = cfg.APIKey
		s.mgr.SetLLMConfig(base)
		if s.settings != nil {
			_ = s.settings.SetLLM(base)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(base))
	} else if base.Provider == "" || base.Provider == "mock" {
		s.mgr.SetLLMConfig(cfg)
		if s.settings != nil {
			_ = s.settings.SetLLM(cfg)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(cfg))
	}
	s.mgr.ApplyProviderCreds(cfg.Provider, cfg.APIKey, cfg.BaseURL)
	ep := s.endpointFor(ctx, cfg.Provider, cfg, true)
	return ep, nil
}

// LoginQoder 用 Qoder 自己的浏览器授权。本机已经登录过就直接用，否则打开浏览器等用户确认。
func (s *API) LoginQoder(ctx context.Context) (ProviderEndpoint, error) {
	cfg, err := llm.AuthorizeQoder(ctx, func(url string) {
		_ = mygo.Shell.OpenExternal(url)
	})
	if err != nil {
		return ProviderEndpoint{}, err
	}
	cfg.Model = ""
	s.rememberProvider(cfg)
	base := s.mgr.LLMConfig()
	if base.Provider == "" || base.Provider == "mock" || base.Provider == "qoder" {
		next := base
		next.Provider = "qoder"
		next.BaseURL = cfg.BaseURL
		next.APIKey = cfg.APIKey
		if next.Model == "" || base.Provider != "qoder" {
			next.Model = llm.DefaultQoderModel
		}
		s.mgr.SetLLMConfig(next)
		if s.settings != nil {
			_ = s.settings.SetLLM(next)
		}
		LLMConfigEvt.Emit(s.win, llm.Redacted(next))
	}
	s.mgr.ApplyProviderCreds("qoder", cfg.APIKey, cfg.BaseURL)
	return s.endpointFor(ctx, "qoder", cfg, true), nil
}

func (s *API) endpointFor(ctx context.Context, id string, cfg llm.Config, saved bool) ProviderEndpoint {
	ep := ProviderEndpoint{Provider: id, Saved: saved}
	// 豆包 CLI 是本机进程：没有地址和 Key，检查的是「能不能启动、能不能列模型」。
	if id == llm.DoubaoCLIProvider {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ok, ver := llm.DoubaoCLIStatus(cctx)
		if !ok {
			ep.Reason = ver
			return ep
		}
		metas, err := llm.ListDoubaoCLIModels(cctx)
		if err != nil {
			ep.Reason = err.Error()
			return ep
		}
		names := make([]string, len(metas))
		for i, m := range metas {
			names[i] = m.Name
		}
		ep.OK = true
		ep.Quota = ver
		ep.Models = names
		return ep
	}
	if !saved {
		ep.Reason = "未保存"
		return ep
	}
	cfg = llm.ApplyDefaults(cfg)
	ep.BaseURL = cfg.BaseURL
	ep.HasKey = cfg.APIKey != ""
	// Qoder 的模型名单和额度各发一次请求。名单请求失败不能改用内置名单。
	listBudget := 3 * time.Second
	if id == "qoder" {
		listBudget = 15 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	models, err := s.listModelsWithSavedCreds(cctx, cfg)
	if err != nil {
		ep.Reason = err.Error()
		return ep
	}
	if id == "qoder" && cfg.APIKey != "" {
		qctx, qcancel := context.WithTimeout(ctx, 8*time.Second)
		quota, qerr := llm.QoderAccountStatus(qctx, cfg.APIKey)
		qcancel()
		if qerr != nil {
			ep.Reason = qerr.Error()
			return ep
		}
		ep.Quota = quota
	}
	ep.OK = true
	ep.Models = models
	return ep
}

func (s *API) providerCreds() map[string]llm.Config {
	out := map[string]llm.Config{}
	if s.mgr == nil {
		return out
	}
	for _, p := range s.mgr.Providers() {
		if p.Provider == "" || p.Provider == "mock" {
			continue
		}
		out[p.Provider] = llm.ApplyDefaults(p)
	}
	base := s.mgr.LLMConfig()
	if base.Provider == "" || base.Provider == "mock" {
		return out
	}
	prev, ok := out[base.Provider]
	if !ok {
		out[base.Provider] = llm.ApplyDefaults(base)
		return out
	}
	if prev.APIKey == "" {
		prev.APIKey = base.APIKey
	}
	if prev.BaseURL == "" {
		prev.BaseURL = base.BaseURL
	}
	out[base.Provider] = llm.ApplyDefaults(prev)
	return out
}

func (s *API) rememberProvider(cfg LLMConfig) {
	if s.mgr == nil || cfg.Provider == "" || cfg.Provider == "mock" {
		return
	}
	if s.settings != nil {
		saved, err := s.settings.UpsertProvider(cfg)
		if err != nil {
			log.Printf("persist provider: %v", err)
			s.mgr.SetProviders(upsertProviderList(s.mgr.Providers(), cfg))
			return
		}
		s.mgr.SetProviders(saved)
		return
	}
	s.mgr.SetProviders(upsertProviderList(s.mgr.Providers(), cfg))
}

// ListModelsFor 拉取某个会话可用的模型列表。
//
// 关键约束：凭据（BaseURL / APIKey）一律取自【已保存的配置】，
// 不接受前端传进来的草稿。用户在设置里配好 Key 之后，
// 会话里的模型按钮就该直接列出可用模型 —— 而不是再让人填一遍。
//
// provider 参数只用于「想先看看别的 provider 有哪些模型」这种预览场景；
// 留空则用该会话实际生效的 provider。
func (s *API) ListModelsFor(ctx context.Context, agentId, provider string) ([]string, error) {
	cfg := s.mgr.LLMConfigFor(agentId)
	if provider != "" {
		cfg.Provider = provider
	}
	return s.listModelsWithSavedCreds(ctx, cfg)
}

// listModelsWithSavedCreds 用已保存的凭据调 /models。
// 凭据缺失时就地短路并给出可操作的错误，不发无意义请求。
func (s *API) listModelsWithSavedCreds(ctx context.Context, cfg llm.Config) ([]string, error) {
	// 本地 provider 的默认 BaseURL 在这里补齐，
	// 否则「没填 BaseURL」会被误判成未配置 —— 用户明明只要起了服务就能用。
	cfg = llm.ApplyDefaults(cfg)

	switch {
	case cfg.Provider == "" || cfg.Provider == "mock":
		return nil, fmt.Errorf("%w：这个会话还没有模型", llm.ErrNotConfigured)
	case cfg.Provider == llm.DoubaoCLIProvider:
		// CLI 不用地址和 Key，能启动就能列模型。
		return llm.ListModels(ctx, cfg)
	case cfg.BaseURL == "":
		return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 Base URL", llm.ErrNotConfigured, cfg.Provider)
	case cfg.APIKey == "" && !llm.IsLocalProvider(cfg.Provider):
		if cfg.Provider == "qoder" {
			if saved, ok := llm.LocalQoder(); ok {
				cfg.APIKey = saved.APIKey
			}
		}
		if cfg.APIKey == "" && cfg.Provider == "qoder" {
			return nil, fmt.Errorf("%w：请先授权登录 Qoder", llm.ErrNotConfigured)
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("%w：请先在设置里填写 %s 的 API Key", llm.ErrNotConfigured, cfg.Provider)
		}
	}
	return llm.ListModels(ctx, cfg)
}

// ProvidersStatus 返回各 provider 的就绪状态。
func (s *API) ProvidersStatus() []ProviderStatus {
	base := s.mgr.LLMConfig()
	out := []ProviderStatus{
		{
			ID:      "llamacpp",
			Ready:   true,
			BaseURL: cmp.Or(base.BaseURL, llm.DefaultLlamaCppBaseURL),
			Reason:  "本地 llama.cpp，不需要 Key",
		},
		{
			ID:      "ollama",
			Ready:   true,
			BaseURL: cmp.Or(base.BaseURL, llm.DefaultOllamaBaseURL),
			Reason:  "本地 Ollama，不需要 Key",
		},
	}
	cloud := []string{"openai", "deepseek", "claude", "qoder"}
	for _, id := range cloud {
		st := ProviderStatus{ID: id}
		if base.Provider == id {
			st.Model = base.Model
			st.BaseURL = base.BaseURL
			st.HasKey = base.APIKey != ""
		}
		if id == "qoder" && !st.HasKey {
			if saved, ok := s.mgr.SavedProvider("qoder"); ok && saved.APIKey != "" {
				st.HasKey = true
				st.BaseURL = saved.BaseURL
			} else if local, ok := llm.LocalQoder(); ok {
				st.HasKey = true
				st.BaseURL = local.BaseURL
			}
		}
		st.Ready = st.BaseURL != "" && st.HasKey
		switch {
		case st.BaseURL == "":
			st.Reason = "未配置 Base URL"
		case !st.HasKey && id == "qoder":
			st.Reason = "未登录"
		case !st.HasKey:
			st.Reason = "未配置 API Key"
		}
		out = append(out, st)
	}
	return out
}

// Info 返回系统信息。
func (s *API) Info() SystemInfo {
	stamp := BuildStamp
	if stamp == "dev" {
		stamp = fmt.Sprintf("dev@%d", processStartedAt)
	}
	stale, srcTime := StaleBuild()
	return SystemInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		MyGo:       true,
		BuildStamp: stamp,
		Version:    mygo.App.Version(),
		StaleBuild: stale,
		SourceTime: srcTime,
	}
}

// Diagnose 跑一次链路自检：真调一次模型，确认能拿到回复。
// 不改任何状态，可以随时点。
func (s *API) Diagnose(ctx context.Context) Diagnostics {
	cfg := s.mgr.LLMConfig()
	out := Diagnostics{
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		HasKey:    cfg.APIKey != "",
		CheckedAt: time.Now().Unix(),
	}
	info := s.Info()
	out.BuildStamp = info.BuildStamp
	out.StaleBuild = info.StaleBuild

	// 用一个极短的真实调用确认模型可用。
	// 这是唯一能区分「配置看着对但实际连不上」的办法。
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	turn, err := llm.NewChatModel(cfg).Turn(probeCtx, []llm.Message{
		{Role: "user", Content: "只说两个字：通了"},
	}, nil)
	if err != nil {
		out.ModelErr = err.Error()
	} else {
		out.ModelOK = true
		out.ModelReply = strings.TrimSpace(turn.Text)
	}

	for _, a := range s.mgr.List() {
		snap := a.Snapshot()
		out.Agents = append(out.Agents, AgentDiag{
			ID: snap.ID, Name: snap.Name, Backend: snap.Backend,
			Status: string(snap.Status), QueueLen: snap.QueueLen,
			MessageCount: len(snap.Messages), LastError: snap.LastError,
		})
	}

	// 读检查点目录，判断有没有卡在某一轮的运行
	root := filepath.Join(mustDataDir(), "loop", "runs")
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			buf, rerr := os.ReadFile(filepath.Join(root, e.Name()))
			if rerr != nil {
				continue
			}
			var st struct {
				AgentID    string `json:"agentId"`
				Phase      string `json:"phase"`
				Iteration  int    `json:"iteration"`
				StopReason string `json:"stopReason"`
				Checkpoint int64  `json:"checkpointAt"`
				Budget     struct {
					UsedIterations int `json:"usedIterations"`
					UsedToolCalls  int `json:"usedToolCalls"`
				} `json:"budget"`
			}
			if json.Unmarshal(buf, &st) != nil {
				continue
			}
			out.Loops = append(out.Loops, LoopDiag{
				AgentID: st.AgentID, Phase: st.Phase, Iteration: st.Iteration,
				UsedIter: st.Budget.UsedIterations, UsedTools: st.Budget.UsedToolCalls,
				StopReason: st.StopReason, Checkpoint: st.Checkpoint,
			})
		}
	}
	return out
}
