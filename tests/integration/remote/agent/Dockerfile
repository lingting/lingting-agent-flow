# 远程 Agent 集成测试使用的任务环境镜像。
#
# 用途：验证"容器内 Git Clone + 容器内 Pi CLI 执行 + 本机 Session 持久化"链路。
# 该镜像只用于测试，不属于 AgentFlow 的默认环境定义。
#
# 构建方式（在仓库根目录执行）：
#
#   docker build -t agentflow-test-agent:1.0.0 -f tests/integration/remote/agent/Dockerfile .
#
# 也可以把构建放在远程机器上执行，或使用远程 Docker 构建后推送到测试镜像仓库。
#
# 约束：
#   - 基础镜像与 Pi CLI 版本都固定到具体版本，不使用 latest；
#   - 不写入任何凭据，不预装需要人工交互的扩展；
#   - 提供 git 与 sh，满足 SourceProvider 与常驻运行契约的要求。

FROM node:22.19.0-bookworm-slim

LABEL agentflow.test="remote-agent" \
      agentflow.description="AgentFlow 远程 Agent 集成测试环境"

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        openssh-client \
        procps \
        tar \
        unzip \
        xz-utils \
        zip \
    && rm -rf /var/lib/apt/lists/*

# Pi CLI 固定到具体版本，安装方式与官方文档一致。
RUN npm install -g --ignore-scripts @earendil-works/pi-coding-agent@1.3.0 \
    && npm cache clean --force \
    && pi --version

WORKDIR /workspace/project

CMD ["sh", "-c", "sleep infinity"]
