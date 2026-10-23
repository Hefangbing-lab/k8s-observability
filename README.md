# 基于 Kubernetes 的微服务可观测性平台

> 两个 Go 编写的微服务，部署在 Docker Desktop 内置 K8s 集群上，打通 **Metrics（Prometheus + Grafana）** 与 **Logging（Promtail + Loki）** 两大可观测性支柱，并接入 **GitHub Actions** 自动构建镜像。

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
                                     │ HTTP (K8s DNS: user-service)
                          ┌──────────▼──────────┐
                          │   user-service       │
                          │  (:8080, /user/{id}) │
                          └─────────────────────┘
                              │           │
              /metrics 拉取    │           │  stdout 日志
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
| 业务 | Go 1.27，net/http（Go 1.22 路由模式） |
| 容器 | 多阶段 Dockerfile，alpine 运行时 |
| 编排 | Docker Desktop 内置 K8s v1.36 |
| 指标 | Prometheus、kube-prometheus-stack、ServiceMonitor |
| 日志 | Promtail（DaemonSet）、Loki 3.5（tsdb schema） |
| 看板 | Grafana（自建 Dashboard + Explore） |
| CI | GitHub Actions（buildx 多矩阵并行构建） |
| 包管理 | Helm（本地 chart tgz 离线安装） |

## 功能特性

- [x] 两个微服务通过 **K8s Service DNS** 互相调用
- [x] Prometheus 埋点：`http_requests_total` counter + `http_request_duration_seconds` histogram
- [x] **ServiceMonitor** 让 Prometheus Operator 自动发现抓取目标
- [x] Grafana 自建面板：QPS、P99 延迟
- [x] 访问日志中间件统一输出 `method/path/status/duration`
- [x] Promtail 自动采集所有 Pod 日志到 Loki
- [x] Grafana Explore 用 LogQL 检索日志
- [x] GitHub Actions push 代码自动构建镜像推 Docker Hub
- [x] 全部部署文件声明式管理，可 `kubectl apply` 一键恢复

## 项目结构

```
.
├── user-service/              # 订单下游：用户服务
│   ├── main.go                # 含 Prometheus 埋点 + 访问日志中间件
│   ├── Dockerfile             # golang:1.27-alpine 多阶段构建
│   └── go.mod / go.sum
├── order-service/              # 订单服务，通过 http://user-service 调用下游
│   ├── main.go
│   └── Dockerfile
├── k8s/
│   ├── user-service/           # deployment.yaml + service.yaml
│   ├── order-service/          # deployment.yaml + service.yaml
│   ├── ingress.yaml            # /user、/order 路由到对应 Service
│   ├── ingress-nginx/          # controller YAML（国内镜像源适配）
│   └── monitoring/
│       └── servicemonitor.yaml # 两个 ServiceMonitor（release: monitoring 标签）
├── monitoring/
│   └── kube-prometheus-stack-92.10.1.tgz  # 本地 Helm chart
└── .github/workflows/build.yml # CI：push 自动构建并推送镜像
```

## 快速开始

### 1. 启动集群与基础组件

```powershell
# 启动 Docker Desktop 内置 K8s 后：
kubectl get nodes

# 安装监控栈（使用本地 chart，避免网络问题）
helm install monitoring monitoring\kube-prometheus-stack-92.10.1.tgz -n monitoring --create-namespace

# 安装日志栈
helm repo add grafana https://grafana.github.io/helm-charts
helm install loki grafana/loki-stack -n logging --create-namespace --set promtail.enabled=true
```

### 2. 部署业务服务

```powershell
kubectl apply -f k8s\user-service\
kubectl apply -f k8s\order-service\
kubectl apply -f k8s\ingress-nginx\ingress-nginx.yaml
kubectl apply -f k8s\ingress.yaml
kubectl apply -f k8s\monitoring\servicemonitor.yaml
```

### 3. 端口转发访问

```powershell
# Grafana
kubectl port-forward -n monitoring svc/monitoring-grafana 3000:80

# 业务接口
kubectl port-forward -n ingress-nginx svc/ingress-nginx-controller 8088:80
```

- Grafana：http://localhost:3000
- 调接口：`http://localhost:8088/user/3`、`http://localhost:8088/order/100?userId=3`

### 4. 改代码发布

```powershell
cd user-service
docker build -t hefangbing/user-service:v4 .
kubectl set image deployment/user-service user-service=hefangbing/user-service:v4
kubectl rollout status deployment/user-service
```

## 常用 PromQL

```promql
# QPS
sum(rate(http_requests_total[1m])) by (job)

# P99 延迟
histogram_quantile(0.99,
  sum(rate(http_request_duration_seconds_bucket[5m])) by (job, le))

# 错误率
sum(rate(http_requests_total{code=~"5.."}[1m]))
  / sum(rate(http_requests_total[1m]))
```

## 常用 LogQL

```logql
# 查 user-service 全部日志
{container="user-service"}

# 只看业务请求（过滤健康检查噪音）
{container="user-service"} |= "/user/"

# 包含 error 的日志
{container="order-service"} |= "error"
```

## CI/CD

`.github/workflows/build.yml` 在 push 到 master 时触发：

- matrix 并行构建 user-service / order-service
- 登录 Docker Hub（Secrets: `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`）
- 打双标签：`latest` + git commit SHA

## 问题记录

1. **国内镜像拉取**：`registry.k8s.io`、`docker.io` 直连失败，统一换 DaoCloud 镜像源 `m.daocloud.io/...`
2. **Docker 构建时 GOPROXY**：宿主机 `go env -w` 不影响容器，必须在 Dockerfile 里 `ENV GOPROXY=https://goproxy.cn,direct`
3. **Grafana 13 与 Loki 2.6 不兼容**：健康检查 LogQL 语法太新，现场升级 Loki 3.5 并改用 tsdb schema（v13）
4. **StatefulSet 更新策略**：镜像改了 Pod 不重建，需要手动 `kubectl delete pod loki-0`
5. **livenessProbe 污染指标**：健康检查每 5 秒一次，会让 QPS 基线偏高
6. **集群重置恢复**：所有部署声明式管理，靠 git 里的 yaml + 本地 chart tgz 10 分钟恢复

## 面试关键

- 可观测性三支柱中 Metrics 与 Logging 的完整链路
- Prometheus Operator 的工作原理：ServiceMonitor → 自动生成抓取配置
- P99 的计算方式：histogram_quantile + bucket
- 为什么用 stdout 约定而不是应用直接写文件：容器日志管理的最佳实践
- 声明式部署 / IaC 的价值：集群故障可快速重建
- CI/CD 闭环：push → 构建 → 推镜像
