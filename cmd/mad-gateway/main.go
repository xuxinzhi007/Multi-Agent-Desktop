// mad-gateway 是 M3 网关无头进程：HTTP + SSE 入口、会话绑定、入站去重与回执。
//
// 本入口只装配与内核无关的部分（绑定存储、事件总线、HTTP 服务）。
// Session / Approval / Sender 三个端口由主进程装配阶段接到
// internal/agent.Manager、approval.Center 与渠道适配器；在接线之前，
// 相关接口返回明确的 NOT_WIRED 错误，不伪造会话或审批数据。
//
// 用法：
//
//	go run ./cmd/mad-gateway -addr 127.0.0.1:8780 -data-dir ~/.multi-agent-desktop/gateway
//
// 配置优先级：命令行 flag > 环境变量 > 默认值。
// 环境变量：MAD_GATEWAY_ADDR / MAD_GATEWAY_DATA_DIR / MAD_GATEWAY_TOKEN。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"multi-agent-desktop/internal/gateway"
)

func main() {
	var flagAddr, flagDataDir, flagToken string
	flag.StringVar(&flagAddr, "addr", "", "HTTP 监听地址（默认 127.0.0.1:8780，环境变量 MAD_GATEWAY_ADDR）")
	flag.StringVar(&flagDataDir, "data-dir", "", "绑定等持久化数据目录（环境变量 MAD_GATEWAY_DATA_DIR）")
	flag.StringVar(&flagToken, "token", "", "可选 Bearer token；未配置时仅允许回环访问（环境变量 MAD_GATEWAY_TOKEN）")
	flag.Parse()

	cfg := gateway.ResolveConfig(flagAddr, flagDataDir, flagToken)

	binder, err := gateway.NewFileBinder(cfg.DataDir)
	if err != nil {
		log.Fatalf("初始化绑定存储失败：%v", err)
	}
	broker := gateway.NewBroker()

	// 端口留空：主 Agent 装配时通过 gateway.Deps 注入真实实现。
	svc := gateway.NewService(gateway.Deps{
		Binder: binder,
		Broker: broker,
	})

	srv := gateway.NewServer(svc, cfg.Token)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("mad-gateway 监听 %s（数据目录：%s）", cfg.Addr, cfg.DataDir)
		if cfg.Token != "" {
			log.Printf("鉴权：Bearer token 已配置")
		} else {
			log.Printf("鉴权：未配置 token，仅允许本机回环访问")
		}
		log.Printf("注意：会话 / 审批 / 渠道发送端口尚未接线，对应接口返回 NOT_WIRED")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务退出：%v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("收到退出信号，关闭 HTTP 服务…")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭 HTTP 服务出错：%v", err)
	}
}
