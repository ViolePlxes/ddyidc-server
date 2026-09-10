.PHONY: build run test lint clean

# 编译
build:
	go build -o bin/ddyidc-server .

# 运行
run:
	go run . -f etc/config.yaml

# 测试
test:
	go test -race -coverprofile=coverage.out ./...

# 代码检查
lint:
	golangci-lint run ./...

# 生成代码（goctl）
gen-api:
	goctl api go -api api/ddyidc.api -dir .

# 清理
clean:
	rm -rf bin/ coverage.out
