.PHONY: fmt fmt-check vet test test-unit test-local test-pi test-ssh test-e2e test-race build release release-dist release-verify release-notes run web-install web-build web-dev web-verify serve clean check

VERSION ?= dev
# 开发构建使用短提交便于阅读；正式发布制品中的提交由 cmd/agentflow-release 写入完整 SHA。
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
BINARY := bin/agentflow
# 发布制品输出目录与目标平台列表：目标平台留空表示使用 internal/release 中的发布矩阵。
RELEASE_DIR ?= dist/release
# Release 说明写在制品目录之外：发布目录只允许出现清单声明的资产。
RELEASE_NOTES ?= dist/RELEASE_NOTES.md
RELEASE_PLATFORMS ?=
PARALLEL ?= 8
# 前端构建工具链版本：记进制品的版本元数据，正式版本必须能取到真实版本。
NODE_VERSION ?= $(shell node --version 2>/dev/null)
PNPM_VERSION ?= $(shell pnpm --version 2>/dev/null)
# 正式发布的来源信息：只由发布工作流传入，本地构建留空。
SOURCE_REPOSITORY ?=
SOURCE_WORKFLOW ?=
SOURCE_RUN_ID ?=
ATTESTATION_PROVIDER ?=

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


# 并行度跟随 PARALLEL：竞态检测比普通测试更吃内存与 CPU，并行度过高会把 SQLite 迁移与
# 连接建立推到超时（在 18 核机器上以默认 NumCPU 并行会稳定失败），因此显式声明而不是
# 让 go test 按机器核数自行决定。
test-race:
	go test -race -count=1 -p $(PARALLEL) -parallel $(PARALLEL) ./tests/unit/...
	go test -race -count=1 -p $(PARALLEL) -parallel $(PARALLEL) ./tests/integration/local/ssh/...

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
	go run ./cmd/agentflow-release -version "$(VERSION)" -output "$(RELEASE_DIR)" \
		-platforms "$(RELEASE_PLATFORMS)" \
		-node-version "$(NODE_VERSION)" \
		-pnpm-version "$(PNPM_VERSION)" \
		-source-repository "$(SOURCE_REPOSITORY)" \
		-source-workflow "$(SOURCE_WORKFLOW)" \
		-source-run-id "$(SOURCE_RUN_ID)" \
		-attestation-provider "$(ATTESTATION_PROVIDER)"

# release-verify 核验已生成的发布目录：版本与提交一致、平台覆盖完整、资产摘要匹配、
# 目录中没有多余文件。发布工作流在构建之后与下载之后都执行同一入口。
release-verify:
	go run ./cmd/agentflow-release verify-release -dir "$(RELEASE_DIR)" -version "$(VERSION)"

# release-notes 由发布清单生成 Release 说明（要求清单已声明来源信息）。
release-notes:
	go run ./cmd/agentflow-release notes -dir "$(RELEASE_DIR)" -version "$(VERSION)" -output "$(RELEASE_NOTES)"

run: serve

clean:
	rm -rf bin $(RELEASE_DIR)

check: fmt-check vet build
