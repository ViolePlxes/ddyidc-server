# 构建阶段
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ddyidc-server . \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ddyidc-update-helper ./cmd/updatehelper

# 运行阶段
FROM alpine:3.19
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S ddyidc \
    && adduser -S -G ddyidc ddyidc
COPY --from=builder /app/ddyidc-server .
COPY --from=builder /app/ddyidc-update-helper .
RUN mkdir -p /var/lib/ddyidc && chown ddyidc:ddyidc /var/lib/ddyidc
VOLUME ["/var/lib/ddyidc"]
USER ddyidc
EXPOSE 8080
ENTRYPOINT ["./ddyidc-server"]
CMD ["-f", "/etc/ddyidc/config.yaml"]
