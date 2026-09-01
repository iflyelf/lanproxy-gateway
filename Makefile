VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN := lanproxy-gateway

.PHONY: all build test vet clean run tidy cross

all: build

## build: 编译当前平台二进制
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/gateway

## test: 运行单元测试
test:
	go test ./...

## vet: 静态检查
vet:
	go vet ./...

## tidy: 整理依赖
tidy:
	go mod tidy

## run: 前台运行(需要 root)
run: build
	sudo ./bin/$(BIN) run -c ./gateway.example.yaml

## cross: 交叉编译多架构二进制到 dist/
cross:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-linux-amd64 ./cmd/gateway
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-linux-arm64 ./cmd/gateway

## clean: 清理产物
clean:
	rm -rf bin dist
