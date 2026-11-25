package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const qoderCatalogTTL = 5 * time.Minute

type qoderCatalogSnapshot struct {
	at     time.Time
	token  string
	models []qoderModel
}

var (
	qoderCatalogMu   sync.Mutex
	qoderCatalogSnap qoderCatalogSnapshot
)

// ModelMeta 是列表里一个模型的名字和这次拿到的思考参数。
type ModelMeta struct {
	Name      string
	Reasoning bool
	Efforts   []string
}

// ListModelMetas 返回模型名，以及目录里实际带回来的思考档位。
func ListModelMetas(ctx context.Context, cfg Config) ([]ModelMeta, error) {
	if cfg.Provider == DoubaoCLIProvider {
		return ListDoubaoCLIModels(ctx)
	}
	if cfg.Provider == "qoder" {
		cfg = ApplyDefaults(cfg)
		if cfg.APIKey == "" {
			if saved, ok := LocalQoder(); ok {
				cfg.APIKey = saved.APIKey
			}
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("%w", ErrMissingAPIKey)
		}
		models, err := qoderResolve(ctx, cfg)
		if err != nil {
			return nil, err
		}
		out := make([]ModelMeta, len(models))
		for i, m := range models {
			out[i] = ModelMeta{Name: m.id, Reasoning: m.reasoning, Efforts: append([]string(nil), m.efforts...)}
		}
		return out, nil
	}
	names, err := ListModels(ctx, cfg)
	if err != nil {
		return nil, err
	}
	out := make([]ModelMeta, len(names))
	for i, name := range names {
		out[i] = ModelMeta{Name: name, Reasoning: ModelReasons(cfg.Provider, name)}
		if out[i].Reasoning {
			out[i].Efforts = []string{"low", "medium", "high", "xhigh"}
		}
	}
	return out, nil
}

func qoderResolve(ctx context.Context, cfg Config) ([]qoderModel, error) {
	qoderCatalogMu.Lock()
	snap := qoderCatalogSnap
	qoderCatalogMu.Unlock()
	if snap.token == cfg.APIKey && len(snap.models) > 0 && time.Since(snap.at) < qoderCatalogTTL {
		return snap.models, nil
	}
	fetched, err := fetchQoderCatalog(ctx, cfg)
	if err == nil && len(fetched) > 0 {
		qoderCatalogMu.Lock()
		qoderCatalogSnap = qoderCatalogSnapshot{at: time.Now(), token: cfg.APIKey, models: fetched}
		qoderCatalogMu.Unlock()
		return fetched, nil
	}
	if snap.token == cfg.APIKey && len(snap.models) > 0 {
		return snap.models, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("qoder: 模型目录是空的")
}

func qoderCatalogLookup(id string) (qoderModel, bool) {
	qoderCatalogMu.Lock()
	defer qoderCatalogMu.Unlock()
	if len(qoderCatalogSnap.models) == 0 || time.Since(qoderCatalogSnap.at) >= qoderCatalogTTL {
		return qoderModel{}, false
	}
	for _, m := range qoderCatalogSnap.models {
		if m.id == id || m.key == id {
			return m, true
		}
	}
	return qoderModel{}, false
}

func installQoderCatalog(models []qoderModel) func() {
	qoderCatalogMu.Lock()
	prev := qoderCatalogSnap
	qoderCatalogSnap = qoderCatalogSnapshot{at: time.Now(), token: prev.token, models: models}
	qoderCatalogMu.Unlock()
	return func() {
		qoderCatalogMu.Lock()
		qoderCatalogSnap = prev
		qoderCatalogMu.Unlock()
	}
}

func fetchQoderCatalog(ctx context.Context, cfg Config) ([]qoderModel, error) {
	ident, err := qoderIdentity(cfg.APIKey)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultQoderBaseURL
	}
	listURL := base + "/algo/api/v2/model/list?Encode=1"
	headers, err := qoderHeaders(nil, listURL, ident)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("accept", "application/json")
	resp, err := do(httpClient, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qoder: 模型目录 %d", resp.StatusCode)
	}
	return parseQoderCatalog(payload)
}

type catalogEntry struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Enable      *bool  `json:"enable"`
	IsReasoning bool   `json:"is_reasoning"`
	Thinking    *struct {
		Enabled *struct {
			Efforts map[string]struct {
				IsDefault bool `json:"is_default"`
			} `json:"efforts"`
		} `json:"enabled"`
	} `json:"thinking_config"`
}

func (e catalogEntry) model() (qoderModel, bool) {
	if e.Key == "" || e.DisplayName == "" {
		return qoderModel{}, false
	}
	if e.Enable != nil && !*e.Enable {
		return qoderModel{}, false
	}
	m := qoderModel{id: e.DisplayName, key: e.Key, reasoning: e.IsReasoning}
	if e.Thinking != nil && e.Thinking.Enabled != nil {
		m.reasoning = true
		order := []string{"low", "medium", "high", "xhigh", "max"}
		seen := map[string]bool{}
		for _, name := range order {
			spec, ok := e.Thinking.Enabled.Efforts[name]
			if !ok {
				continue
			}
			m.efforts = append(m.efforts, name)
			seen[name] = true
			if spec.IsDefault {
				m.defaultEffort = name
			}
		}
		var extra []string
		for name := range e.Thinking.Enabled.Efforts {
			if !seen[name] {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		m.efforts = append(m.efforts, extra...)
		if m.defaultEffort == "" {
			for _, name := range m.efforts {
				if name == "medium" {
					m.defaultEffort = name
					break
				}
			}
		}
		if m.defaultEffort == "" && len(m.efforts) > 0 {
			m.defaultEffort = m.efforts[0]
		}
	}
	return m, true
}

func parseQoderCatalog(raw []byte) ([]qoderModel, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("qoder: 模型目录解析失败: %w", err)
	}
	if data, ok := root["data"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(data, &inner) == nil {
			if _, has := inner["chat"]; has {
				root = inner
			}
		}
	}
	var out []qoderModel
	seen := map[string]bool{}
	take := func(scene string) {
		rawScene, ok := root[scene]
		if !ok {
			return
		}
		var items []catalogEntry
		if json.Unmarshal(rawScene, &items) != nil {
			return
		}
		for _, item := range items {
			m, ok := item.model()
			if !ok || seen[m.key] {
				continue
			}
			seen[m.key] = true
			out = append(out, m)
		}
	}
	take("chat")
	take("app")
	if len(out) == 0 {
		keys := make([]string, 0, len(root))
		for k := range root {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			take(k)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("qoder: 模型目录是空的")
	}
	return out, nil
}
