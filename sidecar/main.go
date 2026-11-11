// multi-agent-desktop sidecar —— W2
// 本地 HTTP + WebSocket 服务：多 agent 会话、流式 LLM、shell 执行。
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"multi-agent-desktop/sidecar/internal/llm"
	"multi-agent-desktop/sidecar/internal/server"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := flag.String("port", "17389", "sidecar listen port")
	workDir := flag.String("workdir", "", "default working directory for shell commands")
	flag.Parse()

	dir := *workDir
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}

	cfg := llm.Config{
		Provider: envOr("MAD_LLM_PROVIDER", "mock"),
		BaseURL:  envOr("MAD_LLM_BASE_URL", ""),
		APIKey:   envOr("MAD_LLM_API_KEY", ""),
		Model:    envOr("MAD_LLM_MODEL", ""),
	}

	s := server.New(dir, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "multi-agent-sidecar",
			"version": "0.2.0",
			"clients": s.Hub.Count(),
			"agents":  len(s.Agents.List()),
			"llm":     s.LLMConfig(),
			"workDir": s.WorkDir(),
			"ts":      time.Now().Unix(),
		})
	})
	mux.HandleFunc("/ws", s.HandleWS)

	addr := "127.0.0.1:" + *port
	log.Printf("[sidecar] listening on http://%s (workdir=%s, llm=%s)", addr, dir, cfg.Provider)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[sidecar] failed: %v", err)
	}
}
