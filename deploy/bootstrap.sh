#!/usr/bin/env bash
# ============================================================
# InfraGym 一键起真实演练环境
# 前提：本机已安装 docker + kubectl + kind + helm（脚本会检查）
# 效果：起 kind 集群 → 装 chaos-mesh + Prometheus → 部署 demo workload
#       → 编译并启动 control plane → 打开前端 Live 模式
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
CLUSTER_NAME="infragym"
KUBECONFIG_PATH="$HOME/.kube/config"

log()  { printf '\033[36m[INFO]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[WARN]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[FAIL]\033[0m %s\n' "$*" >&2; exit 1; }

check_deps() {
  local missing=()
  for cmd in docker kubectl kind helm go; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ ${#missing[@]} -gt 0 ]; then
    die "缺少依赖: ${missing[*]}。请先安装（见脚本头部说明）。"
  fi
  docker info >/dev/null 2>&1 || die "Docker daemon 未运行，请先启动 Docker Desktop。"
  log "依赖检查通过"
}

create_cluster() {
  if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    log "集群 $CLUSTER_NAME 已存在，跳过创建"
  else
    log "创建 kind 集群..."
    kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/kind-config.yaml"
  fi
  kubectl cluster-info --context "kind-$CLUSTER_NAME" >/dev/null
  log "集群就绪"
}

install_chaos_mesh() {
  if kubectl get ns chaos-mesh >/dev/null 2>&1; then
    log "chaos-mesh 已安装，跳过"
    return
  fi
  log "安装 chaos-mesh..."
  helm repo add chaos-mesh https://charts.chaos-mesh.org >/dev/null 2>&1 || true
  helm repo update chaos-mesh >/dev/null
  helm install chaos-mesh chaos-mesh/chaos-mesh \
    --namespace chaos-mesh --create-namespace \
    --set dashboard.create=false \
    --set controllerManager.resources.requests.cpu=10m \
    --wait --timeout 5m
  log "chaos-mesh 安装完成"
}

deploy_monitoring() {
  log "部署 Prometheus + kube-state-metrics..."
  kubectl apply -f "$SCRIPT_DIR/monitoring.yaml" >/dev/null
  kubectl -n infragym-monitoring rollout status deploy/prometheus --timeout=3m
  kubectl -n infragym-monitoring rollout status deploy/kube-state-metrics --timeout=3m
  log "监控就绪（Prometheus NodePort 30900）"
}

deploy_workload() {
  log "部署 demo workload（6 副本）..."
  kubectl apply -f "$SCRIPT_DIR/demo-workload.yaml" >/dev/null
  kubectl -n infragym-demo rollout status deploy/infragym-demo --timeout=3m
  log "workload 就绪"
}

start_control_plane() {
  log "编译 control plane..."
  (cd "$PROJECT_DIR/live" && go build -o "$PROJECT_DIR/live/infragym-live" ./cmd/infragym-live)

  log "启动 control plane :8088 ..."
  local prom_url="http://127.0.0.1:9090"
  "$PROJECT_DIR/live/infragym-live" \
    --addr :8088 \
    --prometheus "$prom_url" \
    --namespace infragym-demo \
    --app-label infragym-demo \
    >/tmp/infragym-live.log 2>&1 &
  echo $! >/tmp/infragym-live.pid

  for i in $(seq 1 20); do
    if curl -sf http://127.0.0.1:8088/api/health >/dev/null 2>&1; then
      log "control plane 健康"
      return
    fi
    sleep 1
  done
  warn "control plane 启动超时，查看日志: /tmp/infragym-live.log"
  cat /tmp/infragym-live.log
}

main() {
  log "InfraGym 真实集群环境启动"
  check_deps
  create_cluster
  install_chaos_mesh
  deploy_monitoring
  deploy_workload
  start_control_plane
  echo
  log "全部就绪！下一步："
  echo "  1. 打开前端 index.html，切到「真实集群」模式"
  echo "  2. 点「注入节点失联」，观察 chaos-mesh 真实杀 Pod、Prometheus 真实掉指标"
  echo "  3. 停止: bash $SCRIPT_DIR/teardown.sh（如需彻底删除集群）"
}

main "$@"
