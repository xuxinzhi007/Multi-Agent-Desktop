package gateway

import (
	"os"
	"path/filepath"
)

// Config 是网关进程的启动配置。命令行 flag 优先，其次环境变量，最后默认值。
type Config struct {
	// Addr 是 HTTP 监听地址，如 127.0.0.1:8780。
	Addr string
	// DataDir 是绑定等持久化数据目录。
	DataDir string
	// Token 是可选 Bearer token。为空时只允许回环地址访问。
	Token string
}

// 环境变量名。
const (
	EnvAddr    = "MAD_GATEWAY_ADDR"
	EnvDataDir = "MAD_GATEWAY_DATA_DIR"
	EnvToken   = "MAD_GATEWAY_TOKEN"
)

// DefaultAddr 是未配置监听地址时的默认值。只绑回环，避免裸奔到局域网。
const DefaultAddr = "127.0.0.1:8780"

// DefaultDataDir 返回默认数据目录：~/.multi-agent-desktop/gateway。
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".gateway"
	}
	return filepath.Join(home, ".multi-agent-desktop", "gateway")
}

// ResolveConfig 把 flag 传入值、环境变量、默认值合并成最终配置。
// flag 传入空串时回落环境变量，再回落默认值。
func ResolveConfig(flagAddr, flagDataDir, flagToken string) Config {
	cfg := Config{
		Addr:    firstNonEmpty(flagAddr, os.Getenv(EnvAddr), DefaultAddr),
		DataDir: firstNonEmpty(flagDataDir, os.Getenv(EnvDataDir), DefaultDataDir()),
		Token:   firstNonEmpty(flagToken, os.Getenv(EnvToken), ""),
	}
	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
