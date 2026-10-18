
FROM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o user-service .


FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/user-service .
EXPOSE 8080
ENTRYPOINT ["./user-service"]
