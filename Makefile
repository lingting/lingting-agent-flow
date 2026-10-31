.PHONY: fmt fmt-check vet test test-unit test-local test-pi test-ssh test-e2e test-packaging test-release-verify test-race build release release-dist run web-install web-build web-dev web-verify serve clean check

VERSION ?= dev
# 开发构建使用短提交便于阅读；正式发布制品中的提交由 cmd/agentflow-release 写入完整 SHA。
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
BINARY := bin/agentflow
# 发布制品输出目录与目标平台列表：留空表示使用 internal/release 中的发布矩阵。
RELEASE_DIR ?= dist/release
RELEASE_PLATFORMS ?=
PARALLEL ?= 8

fmt:
	gofmt -w cmd internal tests

fmt-check:
	@gofmt -l cmd internal tests

vet:
	go vet ./...

test:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/...

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

# web-verify 校验前端产物完整性：缺少入口页面、脚本或样式时构建立即失败，
# 避免产出"编译成功但页面空白"的可执行文件。
web-verify:
	@test -f internal/webui/dist/index.html || (echo "错误: 前端构建产物缺少 index.html，请执行 make web-build" && exit 1)
	@ls internal/webui/dist/assets/*.js >/dev/null 2>&1 || (echo "错误: 前端构建产物缺少脚本资源" && exit 1)
	@ls internal/webui/dist/assets/*.css >/dev/null 2>&1 || (echo "错误: 前端构建产物缺少样式资源" && exit 1)

# web-dev 启动前端开发服务器（另开一个终端运行 make serve 提供 API）。
web-dev:
	cd lingting-agent-flow-ui && pnpm dev

# serve 启动完整应用服务（HTTP API、内嵌页面与后台服务）。
# 开发模式下页面由 Vite+ 提供，因此关闭内嵌产物的强校验：全新检出也能直接启动后端。
serve:
	go run ./cmd/agentflow server --require-web-ui=false

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

# release 是唯一推荐的单文件生产构建入口：先构建前端，再编译 Go，
# 保证二进制中的 UI 与当前源码一致；顺序不可颠倒。
release: web-build web-verify
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentflow

# release-dist 生成跨平台发布制品：先构建并校验前端产物，再交叉编译各目标平台，
# 最后写出压缩包、SHA256SUMS 与 release-manifest.json 并自检。
# 正式版本必须通过 VERSION=vMAJOR.MINOR.PATCH 传入；缺省值 dev 只用于本地核对打包流程。
release-dist: web-build web-verify
	go run ./cmd/agentflow-release -version "$(VERSION)" -output "$(RELEASE_DIR)" -platforms "$(RELEASE_PLATFORMS)"

# test-release-verify 执行发布制品的端到端打包验收：真实生成全部目标平台制品并校验
# 压缩包布局、版本元数据与 SHA-256。需要 pnpm 与已安装的前端依赖，否则用例自行跳过。
test-release-verify:
	go test -count=1 -v -run 'TestReleaseArtifacts|TestSingleBinaryPackaging' ./tests/e2e/packaging/...
run: serve

clean:
	rm -rf bin $(RELEASE_DIR)

check: fmt-check vet build
