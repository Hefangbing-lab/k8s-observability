# 阶段一：构建（两个服务保持完全一致，便于统一维护）
FROM golang:1.27-alpine AS builder
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server .

# 阶段二：运行（固定基础镜像版本，不用 latest；非 root 运行）
FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/server ./server
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["./server"]
