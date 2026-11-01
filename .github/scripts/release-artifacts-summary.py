#!/usr/bin/env python3
"""由 release-manifest.json 生成发布制品摘要（Markdown）。

用法：release-artifacts-summary.py <制品目录>

* 只读取 <制品目录>/release-manifest.json，并列出清单声明的制品名称、大小与 SHA-256；
* 摘要打印到标准输出，同时在设置了 GITHUB_STEP_SUMMARY 时追加到 Job 摘要；
* 不打印仓库路径、环境变量、凭据或清单之外的文件。

退出码：0 生成成功；1 制品目录或清单不可读；2 参数缺失。
"""

import json
import os
import pathlib
import sys


def render(directory):
    """把制品目录里的清单渲染为 Markdown 行列表。"""
    manifest_path = directory / "release-manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    lines = ["### 发布制品", "", "| 制品 | 大小（字节） | SHA-256 |", "| --- | --- | --- |"]
    for artifact in manifest["artifacts"]:
        lines.append(
            "| `{0}` | {1} | `{2}` |".format(
                artifact["file"], artifact["size"], artifact["sha256"]
            )
        )
    return lines


def main():
    """生成摘要并以进程退出码报告结果。"""
    if len(sys.argv) != 2:
        print("用法: release-artifacts-summary.py <制品目录>")
        return 2
    directory = pathlib.Path(sys.argv[1])
    if not directory.is_dir():
        print("错误: 制品目录不存在: {0}".format(directory))
        return 1
    try:
        lines = render(directory)
    except (OSError, ValueError, KeyError) as error:
        print("错误: 读取发布清单失败: {0}".format(error))
        return 1
    summary = "\n".join(lines) + "\n"
    sys.stdout.write(summary)
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        with open(summary_path, "a", encoding="utf-8", newline="\n") as handle:
            handle.write(summary)
    return 0


if __name__ == "__main__":
    sys.exit(main())
