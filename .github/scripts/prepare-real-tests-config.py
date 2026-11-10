#!/usr/bin/env python3
"""为 CI 的真实外部依赖测试生成 tests/config/*.yml。

输入只有环境变量：连接目标与凭据来自 real-tests 环境的 Environment Secrets。
输出全部落在工作区里被 .gitignore 忽略的路径：

* tests/config/ssh.yml、docker.yml、pi.yml、git.yml：测试套件读取的配置；
* $RUNNER_TEMP/agentflow-real-tests/：私钥与 known_hosts 临时文件（权限 0600）。

约束：

* 缺少必需 Secret 时写出 ``configured=false`` 并正常退出，由工作流把该次运行标记为
  SKIP：fork PR 拿不到环境 Secret，这是预期情况，不是失败；
* 配置内容与凭据值不打印，只输出"哪些文件已生成"，并对凭据值调用 add-mask；
* 不写入 tests/config 之外的项目路径，也不上传任何配置文件。
"""

import json
import os
import pathlib
import sys

# 必需 Secret：缺少任意一项都无法建立真实 SSH 连接，因此整体跳过。
REQUIRED_SECRETS = (
    "REAL_SSH_HOST",
    "REAL_SSH_USER",
    "REAL_SSH_PRIVATE_KEY",
    "REAL_SSH_KNOWN_HOSTS",
)

# 需要 add-mask 的凭据型输入。
MASKED_SECRETS = (
    "REAL_SSH_PRIVATE_KEY",
    "REAL_SSH_KNOWN_HOSTS",
    "REAL_DOCKER_PI_API_KEY",
    "REAL_GIT_SSH_KEY",
)

# 默认测试镜像与默认模型：与 tests/config/example.*.yml 保持一致，避免使用付费模型。
DEFAULT_TEST_IMAGE = "agentflow-test-agent:1.0.0"
DEFAULT_PI_MODEL = "group/free"

CONFIG_DIR = pathlib.Path("tests/config")


def value(name):
    """返回环境变量原值（保留换行，用于私钥等多行内容）。"""
    return os.environ.get(name) or ""


def trimmed(name):
    """返回去掉首尾空白的环境变量值。"""
    return value(name).strip()


def mask_secret(name):
    """对凭据值调用 add-mask，避免任何后续输出意外暴露它。"""
    raw = value(name)
    if not raw.strip():
        return
    # 只按行调用 add-mask：工作流命令按行解析，多行值整体传给 add-mask 会把后续行
    # 当作普通日志输出，反而泄露凭据内容。
    for line in raw.splitlines():
        if len(line.strip()) >= 8:
            print("::add-mask::" + line.strip(), flush=True)


def set_output(name, text):
    """追加写入 GitHub Actions 输出文件。"""
    path = os.environ.get("GITHUB_OUTPUT")
    if not path:
        return
    with open(path, "a", encoding="utf-8") as handle:
        handle.write("{0}={1}\n".format(name, text))


def add_summary(text):
    """追加写入 Job 摘要。"""
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if not path:
        return
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(text)


def quote(text):
    """把字符串渲染为 YAML 双引号标量。

    JSON 双引号字符串是合法的 YAML 双引号标量，因此用 json.dumps 保证引号、反斜杠
    与换行不会破坏配置结构，也不需要自己实现转义。
    """
    return json.dumps(text, ensure_ascii=False)


def work_dir():
    """返回本机临时目录，用于存放私钥与 known_hosts。"""
    root = os.environ.get("RUNNER_TEMP") or os.environ.get("TMPDIR") or "/tmp"
    return pathlib.Path(root) / "agentflow-real-tests"


def write_secret_file(name, content):
    """把凭据写入临时文件并返回绝对路径。"""
    directory = work_dir()
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / name
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(content)
    path.chmod(0o600)
    return str(path.resolve())


def write_config(name, lines):
    """写出一个测试配置文件并返回文件名。"""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    path = CONFIG_DIR / name
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines) + "\n")
    return name


def ssh_config(key_path, known_hosts_path, port):
    """生成 ssh.yml 的内容。"""
    return [
        "ssh:",
        "  enable: true",
        "  host: {0}".format(quote(trimmed("REAL_SSH_HOST"))),
        "  user: {0}".format(quote(trimmed("REAL_SSH_USER"))),
        "  port: {0}".format(port),
        "  private_key_path: {0}".format(quote(key_path)),
        "  known_hosts:",
        "    - {0}".format(quote(known_hosts_path)),
    ]


def pi_config():
    """生成 pi.yml 的内容。"""
    return [
        "pi:",
        "  enable: true",
        "  model: {0}".format(quote(trimmed("REAL_PI_MODEL") or DEFAULT_PI_MODEL)),
    ]


def docker_config():
    """生成 docker.yml 的内容，返回 (内容行, 容器内 Pi 配置是否已提供)。"""
    lines = [
        "docker:",
        "  remote_enabled: true",
        "  agent_image: {0}".format(
            quote(trimmed("REAL_DOCKER_AGENT_IMAGE") or DEFAULT_TEST_IMAGE)
        ),
        "  git_image: {0}".format(
            quote(trimmed("REAL_DOCKER_GIT_IMAGE") or DEFAULT_TEST_IMAGE)
        ),
    ]
    container_pi = (
        trimmed("REAL_DOCKER_PI_BASE_URL"),
        trimmed("REAL_DOCKER_PI_API_KEY"),
        trimmed("REAL_DOCKER_PI_MODEL"),
    )
    if not all(container_pi):
        return lines, False
    lines.extend(
        [
            "  pi:",
            "    base_url: {0}".format(quote(container_pi[0])),
            "    api_key: {0}".format(quote(container_pi[1])),
            "    model: {0}".format(quote(container_pi[2])),
        ]
    )
    if trimmed("REAL_DOCKER_PI_THINKING_LEVEL"):
        lines.append(
            "    thinking_level: {0}".format(
                quote(trimmed("REAL_DOCKER_PI_THINKING_LEVEL"))
            )
        )
    return lines, True


def git_config(git_key_path):
    """生成 git.yml 的内容。

    只支持私钥方式：与测试套件的凭证选择顺序一致，token 与账号密码一律留空。
    """
    return [
        "git:",
        "  repository: {0}".format(quote(trimmed("REAL_GIT_REPOSITORY"))),
        "  ssh_key_path: {0}".format(quote(git_key_path)),
        '  token: ""',
        '  username: ""',
        '  password: ""',
    ]


def main():
    """生成配置并以进程退出码报告结果。"""
    for secret_name in MASKED_SECRETS:
        mask_secret(secret_name)

    missing = [name for name in REQUIRED_SECRETS if not trimmed(name)]
    if missing:
        set_output("configured", "false")
        add_summary(
            "### 真实测试配置\n\n"
            "SKIP：real-tests 环境缺少必需 Secret（"
            + "、".join(missing)
            + "），本次没有执行任何真实测试。\n\n"
            "fork PR 默认拿不到环境 Secret；同仓库分支请在 Environment 上补齐配置。\n"
            "未执行的用例不计为通过。\n"
        )
        print("SKIP：real-tests 环境缺少必需 Secret: " + ", ".join(missing))
        return 0

    port = trimmed("REAL_SSH_PORT") or "22"
    if not port.isdigit() or not 0 < int(port) <= 65535:
        print("::error::REAL_SSH_PORT 不是合法端口")
        return 1

    key_path = write_secret_file("ssh_key", value("REAL_SSH_PRIVATE_KEY"))
    known_hosts_path = write_secret_file("known_hosts", value("REAL_SSH_KNOWN_HOSTS"))

    written = [
        write_config("ssh.yml", ssh_config(key_path, known_hosts_path, int(port))),
        write_config("pi.yml", pi_config()),
    ]

    docker_lines, container_pi_ready = docker_config()
    written.append(write_config("docker.yml", docker_lines))

    git_key_path = ""
    if trimmed("REAL_GIT_SSH_KEY"):
        git_key_path = write_secret_file("git_key", value("REAL_GIT_SSH_KEY"))
    written.append(write_config("git.yml", git_config(git_key_path)))

    set_output("configured", "true")
    add_summary(
        "### 真实测试配置\n\n已生成的测试配置："
        + "、".join(written)
        + "。\n\n配置内容与凭据值不写入日志；容器内 Pi 覆盖配置"
        + ("已提供" if container_pi_ready else "未提供（相关用例按缺少配置跳过）")
        + "。\n"
    )
    print("已生成真实测试配置: " + ", ".join(written))
    return 0


if __name__ == "__main__":
    sys.exit(main())
