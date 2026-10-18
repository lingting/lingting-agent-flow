.PHONY: fmt fmt-check vet test test-unit test-local test-pi test-ssh test-e2e test-packaging test-race build release run web-install web-build web-dev web-serve clean check

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
BINARY := bin/agentflow
PARALLEL ?= 8

fmt:
	gofmt -w cmd internal tests

fmt-check:
	@gofmt -l cmd internal tests

vet:
	go vet ./...

test:
	go test -count=1 -p $(PARALLEL) -parallel $(PARALLEL) ./tests/...

test-unit:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/unit/...

test-local:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/integration/local/...

test-pi:
	go test -count=1 -v ./tests/integration/pi/...

test-ssh:
	go test -count=1 -v ./tests/integration/ssh/...

test-e2e:
	go test -count=1 -v ./tests/e2e/...

# test-packaging 是单文件发布冒烟测试：构建前端、编译可执行文件、复制到独立目录后启动，
# 校验页面入口、静态资源与同源 API 均可用。缺少 pnpm 或前端依赖时该入口自行跳过。
test-packaging:
	go test -count=1 -v -run TestSingleBinaryPackaging ./tests/e2e/packaging/...

test-race:
	go test -race -count=1 ./tests/unit/...
	go test -race -count=1 ./tests/integration/local/ssh/...

# web-install 按锁文件安装前端依赖，保证与 pnpm-lock.yaml 一致。
web-install:
	cd lingting-agent-flow-ui && pnpm install --frozen-lockfile

# web-build 执行前端生产构建，产物写入 internal/webui/dist 供 go:embed 使用。
web-build: web-install
	cd lingting-agent-flow-ui && pnpm build

# web-dev 启动前端开发服务器（另开一个终端运行 make web-serve 提供 API）。
web-dev:
	cd lingting-agent-flow-ui && pnpm dev

# web-serve 启动 Go HTTP 服务，开发模式下由前端代理转发 /api 请求。
web-serve:
	go run ./cmd/agentflow web serve

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

# release 是唯一推荐的单文件生产构建入口：先构建前端，再编译 Go，
# 保证二进制中的 UI 与当前源码一致。
release: web-build
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

run:
	go run ./cmd/agentflow web serve

clean:
	rm -rf bin

check: fmt-check vet build
