# 构建阶段
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o ddyidc-server .

# 运行阶段
FROM alpine:3.19
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /app/ddyidc-server .
COPY --from=builder /app/etc ./etc
EXPOSE 8080
CMD ["./ddyidc-server", "-f", "etc/config.yaml"]
