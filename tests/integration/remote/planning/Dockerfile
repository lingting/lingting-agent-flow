# 规划运行时真实验收使用的环境镜像。
#
# 用途：作为只读规划容器的镜像，验证真实只读 rootfs 下的规划角色 Pi 调用。
#
# 构建方式（在仓库根目录执行）：
#
#   docker build -t agentflow-test-planning:1.0.0 \
#     -f tests/integration/remote/planning/Dockerfile \
#     tests/integration/remote/planning
#
# 也可以由测试在远端按需构建；构建基础是 tests/integration/remote/agent 的测试镜像。
#
# 约束：
#   - 不写入任何凭据：模型接入配置由测试在运行期写入容器内的临时可写目录，
#     容器删除后随之消失；
#   - 只改变 Pi 的配置目录位置：规划容器 rootfs 只读，唯一可写目录是 /tmp，
#     默认的 $HOME/.pi/agent 无法写入，因此把配置目录指向 /tmp 下的固定路径；
#   - 不引入新的基础镜像版本，也不安装额外依赖。

FROM agentflow-test-agent:1.0.0

LABEL agentflow.test="remote-planning" \
      agentflow.description="AgentFlow 规划运行时真实验收环境"

# 只读 rootfs 下 Pi 的可写配置目录：位于规划容器唯一可写的 /tmp tmpfs 内。
ENV PI_CODING_AGENT_DIR=/tmp/pi-agent

WORKDIR /workspace/project

CMD ["sh", "-c", "sleep infinity"]
