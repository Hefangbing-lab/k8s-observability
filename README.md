# 基于 Kubernetes 的微服务可观测性平台

> 两个 Go 编写的微服务，部署在 Docker Desktop 内置 K8s 集群上，打通可观测性三支柱：**Metrics（Prometheus + Grafana）**、**Logging（Promtail + Loki）**、**Tracing（OpenTelemetry + Collector + Tempo）**，并通过 **GitHub Actions + Kustomize** 把「构建产物 → 部署声明」连成闭环。

## 架构

```
                          ┌─────────────────────┐
   HTTP 请求 ───────────▶ │   ingress-nginx      │
                          └──────────┬──────────┘
                                     │
                          ┌──────────▼──────────┐
                          │   order-service      │
                          │  (:8080, /order/{id})│
                          └──────────┬──────────┘
                                     │ HTTP (K8s DNS: user-service) + traceparent + X-Request-ID
                          ┌──────────▼──────────┐
                          │   user-service       │
                          │  (:8080, /user/{id}) │
                          └─────────────────────┘
                              │        │        │
              /metrics 拉取    │        │        │  OTLP/HTTP 4318
                              ▼        ▼        ▼
                    ┌──────────────┐ ┌────────┐ ┌───────────────┐
                    │ Prometheus   │ │Promtail│ │ otel-collector│
                    │ (Operator)   │ │(Daemon)│ │  batch + 扇出  │
                    └──────┬───────┘ └───┬────┘ └──────┬────────┘
                           │             ▼             │ OTLP/gRPC 4317
                    ┌──────▼───────┐ ┌────────┐ ┌──────▼───────┐
                    │   Grafana    │◀│  Loki  │ │    Tempo     │
                    │ 指标/日志/trace│ └────────┘ └──────────────┘
                    └──────────────┘   ▲ 按 traceID 跳转
```

## 技术栈

| 层次 | 技术 |
|---|---|
| 业务 | Go 1.27，`net/http`（Go 1.22 路由模式）、`log/slog` JSON 日志 |
| 容器 | 多阶段 Dockerfile，`alpine:3.20` 固定版本、非 root 运行 |
| 编排 | Docker Desktop 内置 K8s；`kubectl apply -k k8s/`（Kustomize） |
| 指标 | Prometheus、kube-prometheus-stack、ServiceMonitor、PrometheusRule |
| 日志 | Promtail（DaemonSet）、Loki 3.5（tsdb schema v13） |
| 追踪 | OpenTelemetry SDK（`otelhttp` + OTLP/HTTP）、Collector（batch + 扇出）、Tempo（单机 local 存储） |
| 看板 | Grafana（Dashboard 以 ConfigMap 纳入 git + sidecar 自动加载） |
| CI | GitHub Actions：测试门禁 → 构建推镜像 → 回写镜像 tag 到 git |

## 功能特性

- [x] 两个微服务通过 **K8s Service DNS** 互相调用，带超时、context 取消与连接池调优
- [x] Prometheus 埋点：`http_requests_total` counter + `http_request_duration_seconds` histogram，标签 `code/method/handler`（`handler` 是路由模板，低基数）
- [x] **管理端点与业务链路分离**：`/metrics`、`/health` 不进埋点与访问日志，探针不再污染 QPS/P99
- [x] `X-Request-ID` 与 **W3C traceparent** 双透传：无入站 ID 时 request ID 直接用 trace ID，日志与 trace 共用同一标识
- [x] 分布式追踪：`otelhttp` 自动生成 server/client span，跨服务父子上报，Tempo 里可看两跳瀑布图
- [x] 结构化 JSON 日志（`method/handler/status/duration_ms/request_id`），LogQL 可做数值聚合
- [x] ServiceMonitor 自动发现 + PrometheusRule 告警（P99、5xx 错误率）
- [x] Grafana Dashboard 以 ConfigMap 纳入 git，集群重建不丢
- [x] readiness / startup / liveness 三探针、资源限制、安全上下文、优雅关闭（`Shutdown` + `preStop`）
- [x] CI：测试门禁 → 构建并推送 `latest` + commit SHA → 把 SHA 写回 `k8s/kustomization.yaml`
- [x] 单元测试覆盖：指标不污染、requestID 透传、下游失败 502、超时 504、输入校验 400、**traceparent 延续与跨服务父子关系**、**OTLP 真实上报（protobuf 解码断言）**

## 项目结构

```
.
├── user-service/            # main.go, otel.go, main_test.go, trace_test.go, Dockerfile, go.mod/go.sum
├── order-service/          # 同上；通过 http://user-service 调用下游
├── k8s/
│   ├── kustomization.yaml   # 唯一部署入口（含 images transformer）
│   ├── user-service/         # deployment.yaml + service.yaml
│   ├── order-service/       # deployment.yaml + service.yaml
│   ├── ingress.yaml         # /user、/order 路由
│   ├── ingress-nginx/       # controller YAML（单独 apply，含不可变 Job）
│   ├── networkpolicy.yaml   # 只允许 ingress-nginx / monitoring 访问业务 Pod
│   ├── observability/
│   │   ├── tempo.yaml           # Tempo（ConfigMap + Deployment + Service）
│   │   └── otel-collector.yaml  # Collector（OTLP 接收 → 批处理 → Tempo）
│   └── monitoring/
│       ├── servicemonitor.yaml
│       ├── prometheus-rule.yaml
│       └── dashboard-configmap.yaml
├── monitoring/
│   ├── kube-prometheus-stack-92.10.1.tgz  # 本地 Helm chart（离线可重装）
│   ├── kps-values.yaml      # 监控栈配置（retention、selector、dashboard sidecar）
│   └── loki-values.yaml    # 日志栈配置（Loki 版本 + schema v13/tsdb）
└── .github/workflows/
    ├── build.yml            # CI：test → build → update-manifests
    └── deploy.yml          # 手动把声明式配置应用到集群
```

## 快速开始

命令分两套写法：bash（Git Bash / macOS / Linux）与 PowerShell（Windows）。**不要把 bash 的 `\` 续行和 `| base64 -d` 贴进 PowerShell**，会直接报错。

### 1. 启动集群与基础组件

```bash
# Git Bash / macOS / Linux
kubectl get nodes

# 监控栈（配置写进 git，可复现；release 名必须是 monitoring）
helm install monitoring monitoring/kube-prometheus-stack-92.10.1.tgz \
  -n monitoring --create-namespace -f monitoring/kps-values.yaml

# 国内网络拉不动 docker.io / registry.k8s.io 时，叠加镜像加速覆盖层（可反复 upgrade）：
#   helm upgrade monitoring monitoring/kube-prometheus-stack-92.10.1.tgz \
#     -n monitoring -f monitoring/kps-values.yaml -f monitoring/kps-values.mirror.yaml

# 日志栈（版本与 schema 写进 git，避免重装后版本回退）
helm repo add grafana https://grafana.github.io/helm-charts
helm upgrade --install loki grafana/loki-stack -n logging --create-namespace \
  -f monitoring/loki-values.yaml

# Grafana 密码（chart 生成随机密码，不要猜默认值）
kubectl -n monitoring get secret monitoring-grafana \
  -o jsonpath='{.data.admin-password}' | base64 -d; echo
```

```powershell
# Windows PowerShell：整条命令写一行（或用反引号 ` 续行，不能用 \）
kubectl get nodes
helm install monitoring monitoring/kube-prometheus-stack-92.10.1.tgz -n monitoring --create-namespace -f monitoring/kps-values.yaml
helm repo add grafana https://grafana.github.io/helm-charts
helm upgrade --install loki grafana/loki-stack -n logging --create-namespace -f monitoring/loki-values.yaml

# Grafana 密码：PowerShell 没有 base64 -d，用这两行
$pwdB64 = kubectl -n monitoring get secret monitoring-grafana -o jsonpath='{.data.admin-password}'
[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($pwdB64))
```

### 2. 部署业务服务

```bash
# ingress controller 只装一次（文件里含不可变 Job，不适合反复 apply -k）
kubectl apply -f k8s/ingress-nginx/ingress-nginx.yaml

# 其余全部声明在 k8s/kustomization.yaml，可重复执行（幂等）
kubectl apply -k k8s/
```

### 3. 端口转发访问

```bash
kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80
kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 8088:80
```

- Grafana：http://localhost:3000
- 接口：`http://localhost:8088/user/3`、`http://localhost:8088/order/100?userId=3`
- 验证指标：`kubectl -n default port-forward deploy/user-service 8081:8080` 后 `curl localhost:8081/metrics`

### 4. 一键打开 Grafana 四张图

重启电脑或重连集群后端口转发会失效，Grafana 就“打不开”了。Windows 下直接执行批处理：

```bat
scripts\open-grafana.cmd
```

它会：打开两个 `kubectl port-forward` 窗口（3000 / 18080）→ 读取并解码 Grafana 随机密码 → 打印账号密码 → 自动打开面板。**关掉那两个 kubectl 窗口即停止转发。**

> 为什么不提供 `.ps1`：火绒会把「无限循环 + 后台 Job」的 PowerShell 脚本判为 `Trojan/PS.LoopTasks.a`（启发式误报，不是真病毒）。批处理没有这些特征，也不受 PowerShell 执行策略限制。

macOS / Git Bash：

```bash
bash scripts/open-grafana.sh
```

手动方式（任何 shell 都适用）：

| 目的 | 命令 |
|---|---|
| Grafana | `kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80` |
| 业务入口 | `kubectl -n default port-forward svc/order-service 18080:80` |
| Prometheus | `kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090` |

制造流量（曲线才会动）。**要点：先关掉进度条，否则每个请求都被 `Write-Progress` 拖慢到 200ms 以上**：

```powershell
$ProgressPreference = 'SilentlyContinue'   # 去掉那条一直闪的蓝条，单请求从 ~200ms 降到 ~10ms
1..600 | ForEach-Object {
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 "http://localhost:18080/order/100?userId=3" | Out-Null
  Start-Sleep -Milliseconds 50             # 不睡就是几千 QPS 的瞬时脉冲，图上只有一根竖线
}
```

```powershell
# 更快的等价写法（curl.exe 是 Windows 10+ 自带的，注意必须写 curl.exe，PowerShell 里 curl 是 Invoke-WebRequest 的别名）
$ProgressPreference = 'SilentlyContinue'
1..600 | ForEach-Object { curl.exe -s -o NUL "http://localhost:18080/order/100?userId=3"; Start-Sleep -Milliseconds 50 }
```

```bash
# Git Bash / macOS / Linux
for i in $(seq 1 600); do curl -s "http://localhost:18080/order/100?userId=3" >/dev/null; sleep 0.05; done
```

> 看短时压测要把时间范围调到 **Last 15 minutes** 甚至 **Last 5 minutes**：1 小时窗口里，90 秒的流量脉冲只占 2.5% 宽度，视觉上几乎看不出来。

面板地址：http://localhost:3000/d/k8s-obs-services?from=now-15m&to=now&refresh=10s

四张图对应关系：

| 面板 | PromQL 要点 | 看不到数据时先查 |
|---|---|---|
| QPS by service | `sum by (job) (rate(http_requests_total[1m]))` | 是否打过流量 |
| P99 latency | `histogram_quantile(0.99, sum by (job, le) (rate(..._bucket[5m])))` | 窗口内是否有 ≥2 个样本 |
| 5xx error rate | 分子用 `or ... * 0` 补零，避免空向量 | 无 5xx 时应显示 0 而不是 No data |
| QPS by handler | `sum by (handler) (...)` | 图例若是 `Value`，说明跑的是没有 `handler` 标签的旧镜像 |


## 发布流程（CI/CD 闭环）

```
push 到 master
  └─ build.yml
       ├─ test job          go vet + go test -race（失败则后面全部不跑）
       ├─ build job         多平台构建，推送 <service>:latest 与 :<commit SHA>
       └─ update-manifests  kustomize edit set image ...:<SHA> → commit 回 git
              ↓
       git 里的 k8s/kustomization.yaml 成为镜像版本的唯一事实源
              ↓
       Argo CD / Flux 自动同步，或手动：kubectl apply -k k8s/
       （也可用 .github/workflows/deploy.yml 手动触发，需 Secret: KUBECONFIG_B64）
```

设计要点：

- 镜像 tag 用 **commit SHA**（不可变、可追溯），`latest` 只用于本地演示；不用 `kubectl set image` 直接改集群，避免 git 声明与实际状态漂移。
- `update-manifests` 用 `GITHUB_TOKEN` 推送，GitHub Actions 不会因此再次触发 workflow，不会循环。
- PR 只跑测试与构建、**不推送镜像**（fork 的 PR 读不到 Secrets）。
- 手动 `deploy.yml` 需要长期 kubeconfig，仅适合本地演示；生产应换成 Argo CD/Flux + OIDC 短时凭证。

## 常用 PromQL

```promql
# QPS（按服务）
sum by (job) (rate(http_requests_total[1m]))

# 按接口拆分（handler 是路由模板，不是原始 URL）
sum by (handler) (rate(http_requests_total[1m]))

# P99 延迟：必须先按 le 聚合成累计直方图，再做插值
histogram_quantile(0.99,
  sum by (job, le) (rate(http_request_duration_seconds_bucket[5m])))

# 错误率：用 or 给没有 5xx 样本的 job 补 0，避免分子是空向量导致「无数据」
(
  sum by (job) (rate(http_requests_total{code=~"5.."}[5m]))
  or
  sum by (job) (rate(http_requests_total[5m])) * 0
) / sum by (job) (rate(http_requests_total[5m]))
```

## 常用 LogQL

```logql
# 结构化请求日志
{container="user-service"} | json

# 只看某个接口（/health 与 /metrics 已不产生访问日志）
{container="user-service"} | json | handler="GET /user/{id}"

# 慢请求
{container="order-service"} | json | duration_ms > 100

# 用 request_id 把两个服务的日志串起来
{container=~"user-service|order-service"} | json | request_id="<某个 ID>"

# 从日志算 P99（duration_ms 是数值字段）
quantile_over_time(0.99,
  {container="order-service"} | json | unwrap duration_ms [5m])
```

> Promtail 默认提供 `container` / `namespace` / `pod` 等标签；若要按 `app` 过滤，需要在 Promtail 的 relabel 里把 Pod label 映射出来。

## 链路追踪（Tracing）

### 链路与设计

```
order-service ──OTLP/HTTP:4318──▶ otel-collector ──OTLP/gRPC:4317──▶ Tempo ──HTTP:3200──▶ Grafana
     │  (otelhttp: server span + client span)         (batch + 扇出)
     └── traceparent ──▶ user-service（延续同一 trace，server span 的 parent = 上游 client span）
```

- **应用只认一个端点**：`OTEL_EXPORTER_OTLP_ENDPOINT`（默认 `http://otel-collector.monitoring.svc.cluster.local:4318`），采样、批处理、多后端扇出都放在 Collector，换后端不改代码。
- **采样策略**：`ParentBased(TraceIDRatioBased)`。父级已采样则沿用父级决定，保证一条链路要么全采要么全不采；比例用 `OTEL_TRACES_SAMPLER_ARG` 调（默认 1.0）。
- **span 命名用路由模板**（`GET /order/{id}`）而不是原始 URL：`/order/100`、`/order/101` 会各成一个 span 名，直接在 Tempo 里制造高基数。
- **管理端点是干净的**：`/health`、`/metrics` 在 otelhttp 之外，探针与抓取不产生 span（有单测断言）。
- **ID 统一**：无入站 `X-Request-ID` 时，request ID 直接取 trace ID（32 位 hex）。日志里 `request_id == trace_id`，Loki 与 Tempo 用同一个键互跳。
- **exemplar 依赖 OpenMetrics 协商**：`/metrics` 必须用 `promhttp.HandlerFor(..., HandlerOpts{EnableOpenMetrics: true})`。默认的 `promhttp.Handler()` 只协商 `text/plain;version=0.0.4`，而 exemplar 只有 OpenMetrics 文本格式才有——`ObserveWithExemplar` 写了也导不出去，Grafana 上「指标点 → trace」会静默失效。
- **降级不阻塞**：追踪初始化失败只打一条 warn，业务照常启动——可观测性组件故障不该拖垮主流程。

### 怎么验证真的通了（五步）

```bash
# 1. 起组件（Tempo 与 Collector 都在 k8s/observability/，随 apply -k 一起装）
kubectl apply -k k8s/
kubectl -n monitoring rollout status deploy/otel-collector
kubectl -n monitoring rollout status deploy/tempo

# 2. 打一次请求，响应头里的 X-Request-ID 就是这次请求的 trace ID
curl -s -D - -o /dev/null "http://localhost:18080/order/100?userId=3" | grep -i x-request-id

# 3. 直接查 Tempo API（把上一步的 ID 填进去），应看到两个 service 的 span
kubectl -n monitoring port-forward svc/tempo 3200:3200
curl -s "http://localhost:3200/api/traces/<TRACE_ID>" | python -m json.tool | head -40

# 4. 确认 exemplar 真的在暴露（必须显式要 OpenMetrics 格式，普通 /metrics 看不到）
curl -s -H 'Accept: application/openmetrics-text; version=1.0.0' \
  http://localhost:18080/metrics | grep -m1 'trace_id="'

# 5. 确认 Prometheus 把 exemplar 存下来了（返回 seriesLabels + trace_id 才算通）
kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090
curl -s -G --data-urlencode 'query=http_request_duration_seconds_bucket' \
  --data-urlencode "start=$(date -u -d '-15 min' +%FT%TZ)" \
  --data-urlencode "end=$(date -u +%FT%TZ)" \
  http://localhost:9090/api/v1/query_exemplars
```

Grafana 里看瀑布图：左侧 **Explore** → 数据源选 **Tempo** → 粘贴 trace ID → 两个服务、三到四个 span（`GET /order/{id}`、`HTTP GET`、`GET /user/{id}`）按时间轴排开。

### 从指标和日志跳到 trace

| 入口 | 做法 |
|---|---|
| 指标 → trace | 三件事缺一不可：应用侧 `ObserveWithExemplar` 写 trace ID、`/metrics` 开 `EnableOpenMetrics`、Prometheus 开 `exemplar-storage`。都用 `GET /api/v1/query_exemplars` 验证，Dashboard 的 P99 面板已开 `"exemplar": true`，Grafana 上点 exemplar 点直接进 Tempo（数据源侧 `exemplarTraceIdDestinations` 见 `kps-values.yaml`） |
| 日志 → trace | 日志里的 `trace_id` 字段；装 Loki 后在数据源里加 derived field，指向 Tempo（本次未装 Loki，属可选） |
| trace → 日志 | Tempo 数据源的 `tracesToLogsV2`（同样依赖 Loki，同样可选） |

### TraceQL 示例

```traceql
{ resource.service.name = "order-service" }
{ span.http.route = "GET /order/{id}" && duration > 50ms }
{ status = error }
```

### 这一层的已知取舍

- Tempo 单副本 + `emptyDir` + 24h 保留：Pod 重建即丢历史 trace，生产要换对象存储（S3/OSS）后端。
- 全量采样，没有 tail sampling：压测时会打爆 Collector；生产应在 Collector 加 `tail_sampling` 只留慢请求与错误。
- 未开 Tempo 的 `metrics-generator`：没有 service graph（服务依赖拓扑），需要 span metrics 才有。
- 只采 traces：指标仍走 Prometheus client（pull 模型），没有把 OTel metrics/logs 也接进来——两条路各有取舍，混用会带来两套语义。

## 技术原理与权衡

- **三支柱**：Metrics 回答「多少、多快」，Logging 回答「某一次发生了什么」，Tracing 回答「时间花在哪一跳」。三者靠同一个 trace ID 串起来：日志里的 `request_id`、指标 exemplar 的 `trace_id`、Tempo 的 traceID 完全一致，所以能在 Grafana 里互相跳转。
- **Prometheus Operator**：Prometheus CR 的 `serviceMonitorSelector` 选中 ServiceMonitor → Operator 生成 scrape config 写入 Secret 中的 `prometheus.yaml.gz` → config-reloader 触发热加载。本仓库显式设置了 selector，因此 ServiceMonitor / PrometheusRule 必须带 `release: monitoring` 标签。
- **P99 的准确性**：`histogram_quantile` 假设 bucket 内观测均匀分布，所以默认 `DefBuckets`（5ms 起、间距 2 倍以上）不适合毫秒级内部调用；本项目改用 1ms 起的细粒度 buckets。
- **为什么写 stdout**：容器运行时统一负责日志落盘与轮转，采集（Promtail DaemonSet）与业务解耦，应用不需要管理文件、权限和磁盘。
- **为什么分离管理端点**：把 `/metrics`、`/health` 从被埋点的 handler 里拿掉，比在查询侧用 PromQL 打补丁更彻底；同时避免 livenessProbe 每 5s 制造 0.2 QPS 的假基线。
- **优雅关闭**：K8s 先摘 Endpoint 再发 SIGTERM，二者有传播延迟；应用用 `Server.Shutdown` 等在途请求，Deployment 用 `preStop: sleep 5` 与 `terminationGracePeriodSeconds: 30` 收口。
- **声明式 / GitOps**：YAML 能重建配置，但重建不了 PVC 里的历史数据、Secrets 与集群外状态（见下节）。

## 问题记录（含更正）

1. **国内镜像拉取**：`registry.k8s.io`、`docker.io` 直连失败，ingress controller YAML 统一换成 DaoCloud 镜像源 `m.daocloud.io/...`。
2. **Docker 构建时 GOPROXY**：宿主机 `go env -w` 不影响容器，构建环境必须自带 `ENV GOPROXY=https://goproxy.cn,direct`。
3. **Grafana 新版与 Loki 2.x 不兼容**：默认 `grafana/loki-stack` 装的是 Loki 2.x，健康检查 LogQL 语法不兼容。现在版本与 `schema v13 / tsdb` 固定在 `monitoring/loki-values.yaml`，重装可复现。
4. ~~**StatefulSet 更新策略**：镜像改了 Pod 不重建，需要手动 delete pod~~ —— **此条记录有误，已更正**：StatefulSet 默认 `updateStrategy: RollingUpdate`，改 Pod 模板中的镜像会自动滚动重建。当时的现象更可能来自「只改了 ConfigMap、镜像 tag 未变 + IfNotPresent、或滚动被探针阻塞」。排查应看 `kubectl get sts -o yaml` 与 `kubectl rollout status`，而不是手动删 Pod。
5. **livenessProbe 污染指标**：已修复。管理端点与业务链路分离，并有单元测试断言 `/health`、`/metrics` 不会增加业务计数。
6. **CI 与部署脱节**：已修复。旧版本 CI 推 `latest`/SHA，Deployment 却写死 `:v1` + `IfNotPresent`，两者毫无关系；现在由 `update-manifests` 把 SHA 写回 `k8s/kustomization.yaml`，集群从同一份声明同步。
7. **集群重置恢复**：更正表述 —— git 里的 YAML + chart tgz + values 可以恢复**配置**；Prometheus/Loki 历史数据（PVC）、Docker Hub Secrets、Grafana 用户与数据源改动仍无法靠 YAML 恢复，需要 Velero/快照与密钥管理。
8. **`kubectl set image` 会让 `helm upgrade` 失败**：用命令手改监控组件镜像后，字段归属变成 `kubectl-set`，Helm 走 Server-Side Apply 升级时报 `conflict ... conflict with "kubectl-set" ... containers[name="grafana"].image`。正确做法是把镜像地址下沉到 values（`monitoring/kps-values.mirror.yaml` 这种覆盖层），再删掉被手改的 Deployment 让 Helm 重建；命令式改动一律不进生产。
9. **exemplar 静默丢失（两处坑，都踩过）**：① 应用侧 `promhttp.Handler()` 默认只协商 `text/plain;version=0.0.4`，而 exemplar 只有 OpenMetrics 文本格式才有，代码里写了 `ObserveWithExemplar` 也照样导不出去 → 改用 `promhttp.HandlerFor(..., HandlerOpts{EnableOpenMetrics: true})`，并用单测锁住协商结果与 `trace_id=` 的存在。② Prometheus 侧默认**不存** exemplar（`prometheus_tsdb_exemplar_max_exemplars=0`）→ 在 `kps-values.yaml` 打开 `enableFeatures: [exemplar-storage]`。两处任缺一个，Grafana 的「指标点 → trace」都点不出来，且没有任何报错。

## 容量与压测

压测工具在 `loadtest/`（无第三方依赖的 Go 程序，多阶段构建成 scratch 镜像，约 5MB），以 Job 形式跑在集群内——**不要用本机 `kubectl port-forward` 测吞吐**，单条转发连接会先成为瓶颈。

```bash
docker build -t hefangbing/loadgen:local ./loadtest        # 首次构建
kubectl -n default delete job loadtest --ignore-not-found
kubectl -n default apply -f loadtest/job.yaml             # 50 并发 × 60 秒，打 order-service
kubectl -n default logs job/loadtest
```

实测环境：Docker Desktop 单节点（K8s v1.34，2 vCPU / 8GB），2×order-service + 2×user-service，`/order/{id}` 内部同步调用 `/user/{id}`，数据全部在内存中：

| 指标 | 数值 |
|---|---|
| 总请求 | 348,799（60 秒，0 错误） |
| 吞吐 | **≈5,800 QPS** |
| P50 / P90 / P99 | 1.24ms / 24.6ms / 85.9ms |
| 最大延迟 | 1.34s（GC 与调度抖动导致的长尾） |

读这组数据时的三个前提：① 压测 Pod 与被压服务同节点，不存在跨节点网络开销；② 业务无数据库、无外部依赖，是纯内存 echo 场景，换成真实 IO 会低一到两个数量级；③ 单节点无 HPA，扩容能力未被验证。**这组数字的用处是给出量级与瓶颈位置，不是宣称生产容量。**

## 已知局限与后续计划

| 局限 | 现状 / 计划 |
|---|---|
| Tracing 存储 | 已打通 OTel → Collector → Tempo；但 Tempo 是单机 + emptyDir + 24h 保留，Pod 重建丢历史，生产需对象存储后端与多副本 |
| GitOps 控制器 | `update-manifests` 只更新 git；集群侧需自行安装 Argo CD/Flux，或用 `deploy.yml` 手动同步 |
| 部署凭据 | `deploy.yml` 用长期 kubeconfig（Secret `KUBECONFIG_B64`），生产应换 OIDC |
| NetworkPolicy | Docker Desktop 内置 CNI **确实执行**该策略（实测：策略漏放行服务间互访时，order→user 被丢包，表现为 3s 后 504）；策略已显式放行应用内互访 |
| Ingress | 无 host / TLS / rewrite，仅为本地演示；生产应加 cert-manager 与域名 |
| 单机环境 | 单节点、无 StorageClass 备份、无 HPA/PDB/多可用区演练 |
| REST 语义 | `GET /order/{id}?userId=` 用查询参数表达业务身份，后续应改成资源层级或从认证上下文取 |
| 代码重复 | 两个服务的中间件重复；因 Docker 构建上下文是各自目录，抽公共包需把 context 提到仓库根，收益暂时不足 |
| 容量数据 | 已有集群内压测（见上一节），但只有单节点、无 IO 场景；SLO / Error Budget 仍未定义 |
