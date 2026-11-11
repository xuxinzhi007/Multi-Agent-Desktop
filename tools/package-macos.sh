#!/usr/bin/env bash
# macOS 打包：生成 .app（跳过 DMG，避免 Finder AppleScript 超时）
#
# Tauri 的 bundle_dmg.sh 用 AppleScript 驱动 Finder 排布窗口，
# 在无 GUI 会话 / 无自动化权限时会报 -1712 超时。这里显式只出 .app。
set -euo pipefail

cd "$(dirname "$0")/.."

echo "▶ 1/3 交叉编译 sidecar"
./tools/build-sidecar.sh > /dev/null

echo "▶ 2/3 拷贝到 Tauri externalBin 目录"
mkdir -p src-tauri/binaries
case "$(uname -m)" in
  arm64)  cp sidecar/bin/sidecar-darwin-arm64 src-tauri/binaries/sidecar-aarch64-apple-darwin ;;
  x86_64) cp sidecar/bin/sidecar-darwin-amd64 src-tauri/binaries/sidecar-x86_64-apple-darwin ;;
  *) echo "unsupported arch: $(uname -m)"; exit 1 ;;
esac

echo "▶ 3/3 构建 .app"
npm run tauri build -- --bundles app

APP="src-tauri/target/release/bundle/macos/multi-agent-desktop.app"
echo "✅ 产物: $APP"
du -sh "$APP"
echo ""
echo "安装：拖到 /Applications，或在 Finder 里双击运行"
echo "       open \"$APP\""
