.PHONY: build run test vet lint clean

# 编译
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/ddyidc-server .
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/ddyidc-update-helper ./cmd/updatehelper

# 运行
run:
	go run . -f etc/config.yaml

# 测试
test:
	go test ./...

vet:
	go vet ./...

# 代码检查
lint:
	golangci-lint run ./...

# 生成代码（goctl）
gen-api:
	goctl api go -api api/ddyidc.api -dir .

# 清理
clean:
	rm -rf bin/ coverage.out
