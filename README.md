# InfraGym

一个云基础设施故障演练场。把 IaaS 故障自愈、滚动升级保护、调度重排这类问题，做成可交互、可观测的演练。

**两种模式：**

- **SIM 模拟模式**：零依赖，纯前端确定性模拟。演示故障处理的因果链和策略权衡，打开 `index.html` 即可用。
- **LIVE 真实模式**：Go control plane 连接真实 Kubernetes，用 chaos-mesh 注入故障，从 Prometheus 读真实指标，事件通过 SSE 推给前端。数据不再是"算出来的"，而是"观测出来的"。

## 为什么做这个

单纯跑通 API 说明不了什么。真正难的是把故障处理拆解成状态机、理解每个策略参数背后的权衡，并且让整个系统可复现、可观测。SIM 模式展示第一层（建模能力），LIVE 模式展示第二层（真实工程能力）。

## 快速开始

### SIM 模式

直接用浏览器打开 `index.html`。选场景 → 调策略参数 → 点注入 → 看事件流和结果。

支持用 URL 分享实验：

```
?scenario=node-loss&seed=42&health=1&concurrency=6&retry=5&auto=1
```

锁定 seed 后，用不同策略跑两次并"记录本轮"，右侧会并排对比 MTTR / 影响范围 / 恢复率，并标出最优结果。

### LIVE 模式

需要一台装了 Docker 的机器。一条命令起整套环境：

```bash
cd deploy && bash bootstrap.sh
```

脚本会依次：检查依赖（docker / kubectl / kind / helm / go）→ 创建 kind 集群 → 安装 chaos-mesh → 部署 Prometheus + kube-state-metrics → 部署 6 副本 demo workload → 编译并启动 control plane（`:8088`）。

然后打开前端，切到 LIVE，测试连接，点"注入节点失联"。chaos-mesh 会真的杀掉 Pod，Prometheus 的就绪副本数会真的掉下来，MTTR 从事件时间戳算出来。

清理：`bash deploy/teardown.sh`

目前 LIVE 模式只实现了 S1（pod-kill）。S2 可以接 Argo Rollouts 注入坏镜像，S3 可以接 Descheduler。

无集群也想验证 control plane？`go run ./cmd/infragym-live --mock --addr :8088` 会用 mock 数据源跑通完整链路。

## 架构

```
infragym/
├── index.html / css / js     # 前端：SIM 引擎 + LIVE 客户端 + Canvas 拓扑
├── live/
│   ├── cmd/infragym-live     # control plane 入口
│   └── internal/
│       ├── kube/             # kubeconfig 解析 + K8s REST 客户端（纯 net/http）
│       ├── chaos/            # chaos-mesh CRD 注入与清理
│       ├── metrics/          # Prometheus 查询 + 标准 PromQL
│       ├── analyze/          # MTTR / 检测延迟 / 影响范围聚合
│       └── server/           # HTTP API + SSE
└── deploy/                   # kind / chaos-mesh / Prometheus manifests + 一键脚本
```

几个设计取舍：

- **不用 client-go**：只用到 pods / events / chaos CRD 的读写，直接用 `net/http` 调 REST API，数据链路更透明，也不引入一大坨依赖。
- **一次性同步计算整场演练**：SIM 引擎产出可回放的"剧本"（events + frames），UI 只负责按时间轴播放。逻辑和界面完全解耦，策略对比无需等动画就能拿到结果。
- **确定性 PRNG**：SIM 模式下相同 `(scenario, policy, seed)` 必然产出完全相同的结果。
- **chaos 注入强制 LabelSelector**：并且打 `app.kubernetes.io/managed-by=infragym` 标签，保证能兜底清理，不污染集群。

## 测试

```bash
cd live && go test ./... && go vet ./...
```

覆盖了 MTTR 聚合口径、kubeconfig 解析（token / client-cert 两种认证）、chaos CRD 构造、Prometheus 响应解析。chaos 测试用 `httptest` 起 TLS server 验证请求内容。

## 指标口径

LIVE 模式的 MTTR 从事件时间线聚合：

- 检测延迟 = 第一条 `detect` 事件 − 最后一条 `inject` 事件
- MTTR = 第一条 `recover` 事件 − 最后一条 `inject` 事件
- 影响副本 = 注入时就绪副本数 − 观测到的最小就绪副本数

取"最后一条 inject"是因为基线采样也可能带 inject 语义，真正的故障落地时刻是最后那次注入。这个坑有回归测试守着。
