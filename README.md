# 基于 Kubernetes 的微服务可观测性平台

> 两个 Go 编写的微服务，部署在 Docker Desktop 内置 K8s 集群上，打通 **Metrics（Prometheus + Grafana）** 与 **Logging（Promtail + Loki）** 两条链路，并通过 **GitHub Actions + Kustomize** 把「构建产物 → 部署声明」连成闭环。

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
                                     │ HTTP (K8s DNS: user-service) + X-Request-ID
                          ┌──────────▼──────────┐
                          │   user-service       │
                          │  (:8080, /user/{id}) │
                          └─────────────────────┘
                              │           │
              /metrics 拉取    │           │  stdout(JSON) 日志
                              ▼           ▼
                    ┌──────────────┐  ┌──────────┐
                    │ Prometheus   │  │ Promtail  │
                    │ (Operator)   │  │ (DaemonSet)│
                    └──────┬───────┘  └────┬─────┘
                           │               ▼
                    ┌──────▼───────┐  ┌──────────┐
                    │   Grafana    │◀─│   Loki    │
                    │ QPS/P99/日志  │  │  (3.5)   │
                    └─────────────┘  └──────────┘
```

## 技术栈

| 层次 | 技术 |
|---|---|
| 业务 | Go 1.27，`net/http`（Go 1.22 路由模式）、`log/slog` JSON 日志 |
| 容器 | 多阶段 Dockerfile，`alpine:3.20` 固定版本、非 root 运行 |
| 编排 | Docker Desktop 内置 K8s；`kubectl apply -k k8s/`（Kustomize） |
| 指标 | Prometheus、kube-prometheus-stack、ServiceMonitor、PrometheusRule |
| 日志 | Promtail（DaemonSet）、Loki 3.5（tsdb schema v13） |
| 看板 | Grafana（Dashboard 以 ConfigMap 纳入 git + sidecar 自动加载） |
| CI | GitHub Actions：测试门禁 → 构建推镜像 → 回写镜像 tag 到 git |

## 功能特性

- [x] 两个微服务通过 **K8s Service DNS** 互相调用，带超时、context 取消与连接池调优
- [x] Prometheus 埋点：`http_requests_total` counter + `http_request_duration_seconds` histogram，标签 `code/method/handler`（`handler` 是路由模板，低基数）
- [x] **管理端点与业务链路分离**：`/metrics`、`/health` 不进埋点与访问日志，探针不再污染 QPS/P99
- [x] `X-Request-ID` 跨服务透传，日志可用同一个 ID 串联两端
- [x] 结构化 JSON 日志（`method/handler/status/duration_ms/request_id`），LogQL 可做数值聚合
- [x] ServiceMonitor 自动发现 + PrometheusRule 告警（P99、5xx 错误率）
- [x] Grafana Dashboard 以 ConfigMap 纳入 git，集群重建不丢
- [x] readiness / startup / liveness 三探针、资源限制、安全上下文、优雅关闭（`Shutdown` + `preStop`）
- [x] CI：测试门禁 → 构建并推送 `latest` + commit SHA → 把 SHA 写回 `k8s/kustomization.yaml`
- [x] 单元测试覆盖：指标不污染、requestID 透传、下游失败 502、超时 504、输入校验 400

## 项目结构

```
.
├── user-service/            # main.go, main_test.go, Dockerfile, .dockerignore, go.mod/go.sum
├── order-service/          # 同上；通过 http://user-service 调用下游
├── k8s/
│   ├── kustomization.yaml   # 唯一部署入口（含 images transformer）
│   ├── user-service/         # deployment.yaml + service.yaml
│   ├── order-service/       # deployment.yaml + service.yaml
│   ├── ingress.yaml         # /user、/order 路由
│   ├── ingress-nginx/       # controller YAML（单独 apply，含不可变 Job）
│   ├── networkpolicy.yaml   # 只允许 ingress-nginx / monitoring 访问业务 Pod
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

## 技术原理与权衡

- **三支柱**：本项目做了 Metrics（多少、多快）与 Logging（某次请求发生了什么）；Tracing 尚未实现。`X-Request-ID` 只能串日志，不能替代 span 瀑布图。
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

## 已知局限与后续计划

| 局限 | 现状 / 计划 |
|---|---|
| Tracing 未实现 | 计划用 OpenTelemetry + Tempo，把 `X-Request-ID` 升级为 W3C `traceparent` 并加 exemplar |
| GitOps 控制器 | `update-manifests` 只更新 git；集群侧需自行安装 Argo CD/Flux，或用 `deploy.yml` 手动同步 |
| 部署凭据 | `deploy.yml` 用长期 kubeconfig（Secret `KUBECONFIG_B64`），生产应换 OIDC |
| NetworkPolicy | 需要支持它的 CNI；Docker Desktop 内置 K8s 可能不执行策略 |
| Ingress | 无 host / TLS / rewrite，仅为本地演示；生产应加 cert-manager 与域名 |
| 单机环境 | 单节点、无 StorageClass 备份、无 HPA/PDB/多可用区演练 |
| REST 语义 | `GET /order/{id}?userId=` 用查询参数表达业务身份，后续应改成资源层级或从认证上下文取 |
| 代码重复 | 两个服务的中间件重复；因 Docker 构建上下文是各自目录，抽公共包需把 context 提到仓库根，收益暂时不足 |
| 容量数据 | 尚未做压测与 SLO/Error Budget 定义 |
