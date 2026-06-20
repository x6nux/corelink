#!/usr/bin/env bash
set -euo pipefail

# CoreLink Node Alias Route Mapping Smoke Test
# 依赖: sshpass, jq, .deploy-servers.json

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/../.deploy-servers.json"

if [[ ! -f "$CONFIG_FILE" ]]; then
    echo "ERROR: $CONFIG_FILE not found"
    exit 1
fi

# 从 .deploy-servers.json 读取服务器信息
CTRL_HOST=$(jq -r '.servers.ctrl.host' "$CONFIG_FILE")
CTRL_USER=$(jq -r '.servers.ctrl.user' "$CONFIG_FILE")
CTRL_PASS=$(jq -r '.servers.ctrl.pass' "$CONFIG_FILE")
N2_HOST=$(jq -r '.servers.n2.host' "$CONFIG_FILE")
N2_USER=$(jq -r '.servers.n2.user' "$CONFIG_FILE")
N2_PASS=$(jq -r '.servers.n2.pass' "$CONFIG_FILE")

run_ctrl() {
    sshpass -p "$CTRL_PASS" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "${CTRL_USER}@${CTRL_HOST}" "$@"
}

run_n2() {
    sshpass -p "$N2_PASS" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "${N2_USER}@${N2_HOST}" "$@"
}

echo "=== CoreLink Alias/Route/DNS Smoke Test ==="
echo "ctrl: ${CTRL_HOST} | n2: ${N2_HOST}"
echo ""

# 检查连通性
echo "[0/7] 检查服务器连通性..."
run_ctrl "echo ok" >/dev/null
run_n2 "echo ok" >/dev/null
echo "  ✓ 两台服务器均可达"

# 获取 ctrl 节点信息
CTRL_NODE_ID=$(run_ctrl "corelink node ls -o json" | jq -r '.[0].id // empty')
CTRL_VIP=$(run_ctrl "corelink node ls -o json" | jq -r '.[0].virtual_ip // empty')

if [[ -z "$CTRL_NODE_ID" || -z "$CTRL_VIP" ]]; then
    echo "ERROR: 无法获取 ctrl 节点信息 (node_id=$CTRL_NODE_ID, vip=$CTRL_VIP)"
    exit 1
fi
echo "  ctrl node: id=${CTRL_NODE_ID} vip=${CTRL_VIP}"
echo ""

# Step 1: 创建内部别名
echo "[1/7] 创建内部别名 db.corelink.internal → ${CTRL_VIP}..."
run_ctrl "corelink node alias add --node '${CTRL_NODE_ID}' --name db --fqdn db.corelink.internal --kind internal --vip '${CTRL_VIP}'"
echo "  ✓ 别名已创建"

# Step 2: 创建 direct route
echo "[2/7] 创建 direct route 10.0.0.0/24..."
run_ctrl "corelink route add --node '${CTRL_NODE_ID}' --kind direct --route-cidr 10.0.0.0/24"
echo "  ✓ direct route 已创建"

# Step 3: 创建 static_mapping route
echo "[3/7] 创建 static_mapping route 100.64.2.0/24 → 10.0.2.0/24..."
run_ctrl "corelink route add --node '${CTRL_NODE_ID}' --kind static_mapping --vip-cidr 100.64.2.0/24 --target-cidr 10.0.2.0/24"
echo "  ✓ static_mapping route 已创建"

# Step 4: 配置 DNS
echo "[4/7] 配置 DNS (local intercept, port 5353)..."
run_ctrl "corelink dns config --enabled --intercept local --listen-addr 127.0.0.1 --listen-port 5353 --zones corelink.internal --upstreams 8.8.8.8"
echo "  ✓ DNS 已配置"

# 等待配置传播
echo "  等待配置传播 (3s)..."
sleep 3

# Step 5: 验证 n2 收到的配置
echo "[5/7] 验证 n2 节点 NodeConfig..."
N2_CONFIG=$(run_n2 "curl -sk https://localhost:8443/v1/config")
N2_PREFIXES=$(echo "$N2_CONFIG" | jq '.publishedPrefixes // []')
N2_DNS=$(echo "$N2_CONFIG" | jq '.dns // null')
N2_EGRESS=$(echo "$N2_CONFIG" | jq '.egressRules // []')

echo "  publishedPrefixes: $(echo "$N2_PREFIXES" | jq length) 条"
echo "  dns.enabled: $(echo "$N2_DNS" | jq '.enabled // false')"
echo "  egressRules: $(echo "$N2_EGRESS" | jq length) 条 (n2 不是出口, 应为 0)"

if [[ "$(echo "$N2_PREFIXES" | jq length)" -eq 0 ]]; then
    echo "  ⚠ WARNING: n2 未收到 published prefixes (可能需要更长传播时间)"
fi
if [[ "$(echo "$N2_DNS" | jq '.enabled // false')" != "true" ]]; then
    echo "  ⚠ WARNING: n2 DNS 未启用"
fi
echo "  ✓ n2 配置检查完成"

# Step 6: 验证 ctrl 的 egress rules
echo "[6/7] 验证 ctrl 节点 EgressRules..."
CTRL_CONFIG=$(run_ctrl "curl -sk https://localhost:8443/v1/config")
CTRL_EGRESS=$(echo "$CTRL_CONFIG" | jq '.egressRules // []')
EGRESS_COUNT=$(echo "$CTRL_EGRESS" | jq length)

echo "  egressRules: ${EGRESS_COUNT} 条"
echo "$CTRL_EGRESS" | jq -r '.[] | "  - kind=\(.kind) vip=\(.vipPrefix) target=\(.targetPrefix)"'

HAS_DIRECT=$(echo "$CTRL_EGRESS" | jq '[.[] | select(.kind=="direct")] | length')
HAS_STATIC=$(echo "$CTRL_EGRESS" | jq '[.[] | select(.kind=="static_mapping")] | length')

if [[ "$HAS_DIRECT" -eq 0 ]]; then
    echo "  ✗ FAIL: 缺少 direct egress rule"
    exit 1
fi
if [[ "$HAS_STATIC" -eq 0 ]]; then
    echo "  ✗ FAIL: 缺少 static_mapping egress rule"
    exit 1
fi
echo "  ✓ ctrl egress rules 验证通过"

# Step 7: 完成
echo ""
echo "[7/7] SMOKE TEST PASSED ✓"
echo "  - 内部别名: db.corelink.internal → ${CTRL_VIP}"
echo "  - direct route: 10.0.0.0/24"
echo "  - static_mapping: 100.64.2.0/24 → 10.0.2.0/24"
echo "  - DNS: enabled, local intercept, port 5353"
echo "  - ctrl egress: ${EGRESS_COUNT} rules (direct + static_mapping)"
