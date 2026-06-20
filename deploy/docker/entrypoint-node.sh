#!/bin/bash
set -e

CONTROLLER_HOST="${CONTROLLER_HOST:-controller}"
ADMIN_PORT="${ADMIN_PORT:-8090}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-testpass123}"
NODE_TAG="${NODE_TAG:-node}"
DATA_DIR="/data"
ADMIN_BASE="http://${CONTROLLER_HOST}:${ADMIN_PORT}/admin/api"

mkdir -p "$DATA_DIR"

# api_call <method> <path> [extra_headers...] [--data body]
# 封装 wget 调用 admin API，自动带 JWT token（登录后），3 次重试。
api_call() {
  local method="$1" path="$2"; shift 2
  local url="${ADMIN_BASE}${path}"
  local args=("--timeout=10" "-q" "-O-" "--method=${method}")
  # 收集 headers 和 body
  while [ $# -gt 0 ]; do
    case "$1" in
      --header) args+=("--header=$2"); shift 2 ;;
      --data)   args+=("--body-data=$2"); shift 2 ;;
      *)        shift ;;
    esac
  done
  local attempt=0
  while [ $attempt -lt 3 ]; do
    if result=$(wget "${args[@]}" "$url" 2>/dev/null); then
      echo "$result"
      return 0
    fi
    attempt=$((attempt + 1))
    [ $attempt -lt 3 ] && sleep 1
  done
  echo "[$NODE_TAG] API 调用失败: ${method} ${path}" >&2
  return 1
}

echo "[$NODE_TAG] 等待 controller 就绪..."
until bash -c "echo > /dev/tcp/${CONTROLLER_HOST}/${ADMIN_PORT}" 2>/dev/null; do
  sleep 1
done
sleep 2
echo "[$NODE_TAG] controller 就绪"

# 检查是否已注册
if [ -f "$DATA_DIR/identity.json" ]; then
  echo "[$NODE_TAG] 已注册，跳过 enrollment"
  exec corelink-node -config "$DATA_DIR/node.json"
fi

# 登录获取 JWT token
echo "[$NODE_TAG] 登录 admin API (JWT)..."
TOKEN_RESP=$(api_call POST /login \
  --header "Content-Type: application/json" \
  --data "{\"user\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}") || exit 1
TOKEN=$(echo "$TOKEN_RESP" | jq -r '.token')
if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
  echo "[$NODE_TAG] 获取 token 失败: $TOKEN_RESP"
  exit 1
fi
echo "[$NODE_TAG] JWT token: $(echo "$TOKEN" | cut -c1-20)..."

# 创建 enrollment key（Bearer token 认证）
echo "[$NODE_TAG] 创建 enrollment key..."
KEY_RESP=$(api_call POST /keys \
  --header "Content-Type: application/json" \
  --header "Authorization: Bearer ${TOKEN}" \
  --data "{\"reusable\":false,\"tag\":\"${NODE_TAG}\"}") || exit 1
ENROLL_KEY=$(echo "$KEY_RESP" | jq -r '.key')
CA_HASH=$(echo "$KEY_RESP" | jq -r '.ca_hash')
echo "[$NODE_TAG] enrollment key: $(echo "$ENROLL_KEY" | cut -c1-8)..."
echo "[$NODE_TAG] CA hash: $(echo "$CA_HASH" | cut -c1-20)..."

# 生成 node 配置
cat > "$DATA_DIR/node.json" << EOF
{
  "controller_enroll_addr": "${CONTROLLER_HOST}:7443",
  "controller_mtls_addr": "${CONTROLLER_HOST}:7444",
  "controller_http_addr": "${CONTROLLER_HOST}:8080",
  "enrollment_key": "${ENROLL_KEY}",
  "controller_ca_hash": "${CA_HASH}",
  "data_dir": "${DATA_DIR}",
  "role": "agent"
}
EOF
echo "[$NODE_TAG] 配置已生成"

echo "[$NODE_TAG] 启动 corelink-node..."
exec corelink-node -config "$DATA_DIR/node.json"
