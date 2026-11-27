package x2logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	Logstore    string `json:"logstore"`
	Endpoint    string `json:"endpoint"`
	DisplayName string `json:"displayName"`
	Enabled     bool   `json:"enabled"`
}

type ProjectAlias struct {
	Project string `json:"project"`
	Alias   string `json:"alias"`
}

type QueryRequest struct {
	Source       string `json:"source,omitempty"`
	Environment  string `json:"environment,omitempty"`
	Service      string `json:"service,omitempty"`
	ImageVersion string `json:"imageVersion,omitempty"`
	Instance     string `json:"instance,omitempty"`
	Keyword      string `json:"keyword,omitempty"`
	QueryMode    string `json:"queryMode,omitempty"` // "keyword" | "sls"
	TimeRange    string `json:"timeRange,omitempty"` // "realtime" | "15m" | "1h" | "6h" | "24h" | "7d" | "custom"
	StartTime    string `json:"startTime,omitempty"`
	EndTime      string `json:"endTime,omitempty"`
	Page         int    `json:"page,omitempty"`
	PageSize     int    `json:"pageSize,omitempty"`
}

type LogEntry struct {
	Time     string `json:"time"`
	Level    string `json:"level"`
	Service  string `json:"service"`
	Instance string `json:"instance"`
	Message  string `json:"message"`
}

type QueryResponse struct {
	Logs  []LogEntry `json:"logs"`
	Total int        `json:"total"`
}

type Client struct {
	store *CredStore
	http  *http.Client
}

func NewClient(store *CredStore) *Client {
	if store == nil {
		store = NewCredStore("")
	}
	return &Client{
		store: store,
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *Client) Store() *CredStore { return c.store }

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any, out any) error {
	tok, baseURL, err := c.store.LoadValidToken()
	if err != nil {
		return err
	}
	status, rawResp, reqErr := c.doOnce(ctx, method, baseURL+path, tok, reqBody)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if freshTok, freshBase, herr := c.store.HarvestAndSave(baseURL); herr == nil && freshTok != "" && freshTok != tok {
			status, rawResp, reqErr = c.doOnce(ctx, method, freshBase+path, freshTok, reqBody)
		}
	}
	if reqErr != nil {
		return reqErr
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("devops.x2robot.com 鉴权失败（HTTP %d）：Token 已失效或无该环境日志权限。请用 `/logs auth <BearerToken>` 或 `x2_logs_auth` 更新凭证", status)
	}
	if status < 200 || status >= 300 {
		msg := strings.TrimSpace(string(rawResp))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return fmt.Errorf("请求 %s 失败（HTTP %d）: %s", path, status, msg)
	}
	if out != nil && len(rawResp) > 0 {
		if err := json.Unmarshal(rawResp, out); err != nil {
			return fmt.Errorf("解析 %s 响应失败: %w", path, err)
		}
	}
	return nil
}

func (c *Client) doOnce(ctx context.Context, method, fullURL, token string, reqBody any) (int, []byte, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, */*")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *Client) ListSources(ctx context.Context) ([]Source, map[string]string, error) {
	var sr struct {
		Sources []Source `json:"sources"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/observability/logs/sources", nil, &sr); err != nil {
		return nil, nil, err
	}
	aliases := map[string]string{}
	var ar struct {
		Aliases []ProjectAlias `json:"aliases"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/observability/logs/project-aliases", nil, &ar); err == nil {
		for _, a := range ar.Aliases {
			if a.Project != "" && a.Alias != "" {
				aliases[a.Project] = a.Alias
			}
		}
	}
	return sr.Sources, aliases, nil
}

func (c *Client) ListEnvironments(ctx context.Context, source string) ([]string, error) {
	if strings.TrimSpace(source) == "" {
		source = "default"
	}
	var resp struct {
		Environments []string `json:"environments"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/observability/logs/environments", map[string]string{"source": source}, &resp)
	return resp.Environments, err
}

func (c *Client) ListServices(ctx context.Context, source, environment string) ([]string, error) {
	if strings.TrimSpace(source) == "" {
		source = "default"
	}
	var resp struct {
		Services []string `json:"services"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/observability/logs/services", map[string]string{
		"source":      source,
		"environment": environment,
	}, &resp)
	return resp.Services, err
}

func (c *Client) ListInstances(ctx context.Context, source, environment, imageVersion string) ([]string, error) {
	if strings.TrimSpace(source) == "" {
		source = "default"
	}
	var resp struct {
		Instances []string `json:"instances"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/observability/logs/instances", map[string]string{
		"source":       source,
		"environment":  environment,
		"imageVersion": imageVersion,
	}, &resp)
	return resp.Instances, err
}

func (c *Client) FetchLoginURL(ctx context.Context) (string, error) {
	_, baseURL, _ := c.store.Status()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/auth/login-url", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func normalizeQueryRequest(in QueryRequest) QueryRequest {
	q := in
	if strings.TrimSpace(q.Source) == "" {
		q.Source = "default"
	}
	if strings.TrimSpace(q.QueryMode) == "" {
		q.QueryMode = "keyword"
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 {
		q.PageSize = 50
	} else if q.PageSize > 200 {
		q.PageSize = 200
	}
	if q.TimeRange == "custom" || (q.StartTime != "" && q.EndTime != "") {
		q.TimeRange = ""
	} else if strings.TrimSpace(q.TimeRange) == "" {
		q.TimeRange = "15m"
	}
	return q
}

func (c *Client) QueryLogs(ctx context.Context, req QueryRequest) (QueryResponse, string, error) {
	q := normalizeQueryRequest(req)
	var out QueryResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/observability/logs/query", q, &out); err != nil {
		return QueryResponse{}, "", err
	}
	if len(out.Logs) == 0 && q.QueryMode == "keyword" && q.Keyword != "" {
		trimmed := strings.TrimRight(strings.TrimSpace(q.Keyword), ",，;；")
		if trimmed != "" && trimmed != q.Keyword {
			q2 := q
			q2.Keyword = trimmed
			var out2 QueryResponse
			if err := c.doJSON(ctx, http.MethodPost, "/api/v1/observability/logs/query", q2, &out2); err == nil && len(out2.Logs) > 0 {
				note := fmt.Sprintf("提示：原关键词 %q 未命中，已自动去掉末尾标点按 %q 命中 %d 条日志。", q.Keyword, trimmed, out2.Total)
				return out2, note, nil
			}
		}
	}
	return out, "", nil
}

func FormatQueryResult(res QueryResponse, note string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 16000
	}
	var b strings.Builder
	if note != "" {
		b.WriteString(note)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "共命中 %d 条日志（当前页返回 %d 条）：\n", res.Total, len(res.Logs))
	if len(res.Logs) == 0 {
		b.WriteString("（无匹配日志，可尝试扩大 timeRange 如 1h/6h/24h 或检查 environment/service/keyword）")
		return b.String()
	}
	for i, item := range res.Logs {
		msg := strings.TrimSpace(item.Message)
		if len(msg) > 1500 {
			msg = msg[:1500] + " ...[单条过长已截断]"
		}
		pod := ""
		if item.Instance != "" {
			pod = "[" + item.Instance + "] "
		}
		lvl := item.Level
		if lvl == "" {
			lvl = "LOG"
		}
		line := fmt.Sprintf("%d. %s [%s] %s %s%s\n", i+1, item.Time, lvl, item.Service, pod, msg)
		if b.Len()+len(line) > maxBytes {
			fmt.Fprintf(&b, "... [已达 %d 字节输出上限，省略本页剩余 %d 条日志；请缩小关键词或时间范围]", maxBytes, len(res.Logs)-i)
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}
