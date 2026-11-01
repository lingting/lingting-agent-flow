#!/usr/bin/env python3
"""由 release-manifest.json 生成发布制品摘要（Markdown）。

用法：release-artifacts-summary.py <输出文件>

工作目录必须是发布制品目录（.github/workflows/release.yml 用 working-directory
指定）。只列出制品名称、大小与 SHA-256，不打印仓库路径、环境变量或凭据。
"""

import json
import pathlib
import sys


def main():
    """把摘要写入命令行给出的输出文件。"""
    if len(sys.argv) != 2:
        print("用法: release-artifacts-summary.py <输出文件>")
        return 2
    manifest = json.loads(
        pathlib.Path("release-manifest.json").read_text(encoding="utf-8")
    )
    lines = ["### 发布制品", "", "| 制品 | 大小（字节） | SHA-256 |", "| --- | --- | --- |"]
    for artifact in manifest["artifacts"]:
        lines.append(
            "| `{0}` | {1} | `{2}` |".format(
                artifact["file"], artifact["size"], artifact["sha256"]
            )
        )
    with open(sys.argv[1], "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
