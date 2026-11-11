#!/usr/bin/env bash
# 三端交叉编译 sidecar，产物放到 sidecar/bin/
# Tauri 打包时通过 externalBin 引用这些二进制。
set -euo pipefail

cd "$(dirname "$0")/../sidecar"

mkdir -p bin

echo "▶ darwin/arm64 (Apple Silicon)"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/sidecar-darwin-arm64 .

echo "▶ darwin/amd64 (Intel Mac)"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/sidecar-darwin-amd64 .

echo "▶ windows/amd64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/sidecar-windows-amd64.exe .

echo "▶ linux/amd64"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/sidecar-linux-amd64 .

echo "✅ 产物："
ls -lh bin/
