#!/usr/bin/env bash
# 清理 InfraGym 演练环境：停 control plane、删 chaos 资源、可选删除集群。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
log() { printf '\033[36m[INFO]\033[0m %s\n' "$*"; }

if [ -f /tmp/infragym-live.pid ]; then
  kill "$(cat /tmp/infragym-live.pid)" 2>/dev/null || true
  rm -f /tmp/infragym-live.pid
  log "control plane 已停止"
fi

# 兜底清理可能残留的 chaos 资源，避免影响后续实验
if command -v kubectl >/dev/null 2>&1; then
  kubectl delete podchaos,networkchaos -A -l app.kubernetes.io/managed-by=infragym --ignore-not-found >/dev/null 2>&1 || true
  log "残留 chaos 资源已清理"
fi

read -r -p "是否删除整个 kind 集群 infragym？[y/N] " ans
if [[ "$ans" =~ ^[Yy]$ ]]; then
  kind delete cluster --name infragym
  log "集群已删除"
else
  log "集群保留。下次可直接运行 bootstrap.sh"
fi
