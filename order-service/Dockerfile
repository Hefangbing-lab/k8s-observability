# 阶段一：构建
FROM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o order-service .

# 阶段二：运行
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/order-service .
EXPOSE 8080
ENTRYPOINT ["./order-service"]
