#!/bin/bash
# 给开发版一个固定的代码签名身份。
#
# 为什么需要：mygo dev 找不到签名证书时用 ad-hoc 签名，签名随每次构建变化，
# macOS 把它当成另一个 App —— 于是每次都弹「想要使用你储存在钥匙串里的
# multi-agent-desktop Dev WebCrypto Master Key」，点「始终允许」也没用。
# 固定身份后，授权一次就长期有效（登录态、辅助功能权限同理）。
#
# 用法：
#   bash tools/dev-sign.sh      # 建证书（会弹一次钥匙串授权，输登录密码）
#   npm run dev:signed          # 之后用这个起开发版
set -euo pipefail

NAME="multi-agent-desktop dev"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"

if security find-identity -v -p codesigning | grep -qF "\"$NAME\""; then
  echo "已有签名身份：$NAME"
  echo "直接用 npm run dev:signed 起开发版。"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$tmp/key.pem" -out "$tmp/cert.pem" -days 3650 \
  -subj "/CN=$NAME" \
  -addext "basicConstraints=critical,CA:false" \
  -addext "keyUsage=critical,digitalSignature" \
  -addext "extendedKeyUsage=critical,codeSigning" 2>/dev/null

openssl pkcs12 -export -inkey "$tmp/key.pem" -in "$tmp/cert.pem" \
  -out "$tmp/dev.p12" -passout pass:dev 2>/dev/null

security import "$tmp/dev.p12" -k "$KEYCHAIN" -P dev -T /usr/bin/codesign
security add-trusted-cert -r trustRoot -p codeSign -k "$KEYCHAIN" "$tmp/cert.pem"

echo
echo "已建好签名身份：$NAME"
echo
echo "接下来："
echo "  1. 打开「钥匙串访问」，搜 WebCrypto，删掉旧的 multi-agent-desktop Dev WebCrypto Master Key"
echo "     （它的授权绑在旧的 ad-hoc 签名上，留着还会弹）"
echo "  2. npm run dev:signed"
echo "  3. 第一次弹钥匙串时输密码并点「始终允许」，之后不再弹"
echo
echo "想要更正规的身份：Xcode > Settings > Accounts > Manage Certificates 建一个"
echo "Apple Development 证书（免费），mygo dev 会自动挑它，无需 -sign。"