.PHONY: fmt fmt-check vet inventory test test-unit test-cases test-real test-runner build release release-dist release-verify release-notes run web-install web-build web-dev web-verify serve clean check

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
# 统一测试执行器：集成、E2E 与前端用例都经由它执行，tests/unit 仍由 go test 执行（V3 R8）。
TEST_RUNNER := bin/agentflow-test
# 执行器产物根目录：每次运行落在 <根目录>/<run-id>/，报告与用例日志都在其中。
TEST_ARTIFACT_ROOT ?= .test-artifacts
# test-cases 的选择集合：normal 为全部普通 Case；也可以只跑一个 kind
# （local / e2e / selfcheck / frontend）。真实设备分组用 test-real。
TEST_KIND ?= normal
# 需要被测服务二进制的 Case kind：pilot 试点用例把 <repo>/bin/agentflow 声明为
# service-process 的可执行文件，缺失时资源分配失败并被记为 error（不是跳过）。因此只有这些
# kind 的入口先构建它；其它 kind 不预构建，构建范围不因本规则扩大。
CASE_KINDS_WITH_SERVER_BINARY := normal selfcheck fault
ifeq ($(filter $(TEST_KIND),$(CASE_KINDS_WITH_SERVER_BINARY)),)
TEST_CASES_PREREQUISITES := test-runner
else
TEST_CASES_PREREQUISITES := build test-runner
endif
# inventory 默认写到可写目录：R0 的 legacy-cases.manifest.json 是冻结基线，
# 不得被默认目标覆盖，需要覆盖时必须显式传入 INVENTORY_DIR=.steps/V3/R0。
INVENTORY_DIR ?= $(TEST_ARTIFACT_ROOT)/inventory
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

# inventory 重新生成 V3 R0 的用例清单与资源矩阵：只读盘点，不运行任何测试。
inventory:
	go run ./cmd/agentflow-test-inventory -out "$(INVENTORY_DIR)"

# test 是本地全量入口：单元用 go test，其余已注册的普通 Case 用统一执行器。
# 真实设备分组（test-real）与故障注入分组不在 test 范围内：前者需要单独授权，
# 后者按设计注入失败并以非零退出码结束。
test: test-unit test-cases

test-unit:
	go test -count=1 -v -p $(PARALLEL) -parallel $(PARALLEL) ./tests/unit/...

# test-runner 构建统一测试执行器：worker 由该二进制重新启动自身，每次都要是最新构建。
test-runner:
	go build -o $(TEST_RUNNER) ./cmd/agentflow-test

# test-cases 通过执行器运行 Case；选择条件由 .github/scripts/go-test-groups.sh 提供，
# 本地入口与 CI Job 共用同一份分组声明，不在 Makefile 里另写一份分组清单。
# 前置由 TEST_KIND 决定：需要被测服务的分组先构建 bin/agentflow，其余分组只构建执行器。
test-cases: $(TEST_CASES_PREREQUISITES)
	bash .github/scripts/run-cases.sh --kind "$(TEST_KIND)" --artifact-dir "$(TEST_ARTIFACT_ROOT)" --parallel "$(PARALLEL)"

# test-real 运行真实设备分组：run-cases.sh 要求显式授权（AGENTFLOW_REAL_TESTS_GRANTED=true），
# 未授权时直接失败，不会静默跳过或回退到本机替身。
test-real: test-runner
	bash .github/scripts/run-cases.sh --kind real --artifact-dir "$(TEST_ARTIFACT_ROOT)" --parallel 1
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
