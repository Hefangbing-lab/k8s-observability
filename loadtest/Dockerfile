# 压测工具镜像：静态编译 + scratch，约 5MB
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPROXY=https://goproxy.cn,direct
COPY go.mod ./
RUN go mod download
COPY main.go ./
RUN go build -trimpath -ldflags="-s -w" -o /out/loadtest .

FROM scratch
COPY --from=build /out/loadtest /loadtest
USER 65532:65532
ENTRYPOINT ["/loadtest"]
