package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lingting/agentflow/internal/release"
)

// verifyRelease 核验发布目录并输出结论。
//
// 这一入口由发布工作流在"构建之后、上传之前"以及"下载之后"各执行一次：
// 发布流程不能依赖上传命令的返回值判断资产是否完整。
func verifyRelease(args []string, stdout, stderr io.Writer) error {
	opts, err := parseVerifyOptions(args, stderr)
	if err != nil {
		return err
	}
	if opts.version == "" {
		return fmt.Errorf("%w: 必须显式指定 -version", errUsage)
	}
	manifest, err := release.VerifyRelease(release.ReleaseVerifyOptions{
		Dir:               opts.dir,
		Version:           opts.version,
		Commit:            opts.commit,
		RequireProvenance: opts.requireProvenance,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "发布目录核验通过: 版本 %s 提交 %s 制品 %d 个\n",
		manifest.Version, manifest.Commit, len(manifest.Artifacts))
	return nil
}

// verifyReleaseAssets 核验远端 Release 的实际资产列表与清单完全一致。
func verifyReleaseAssets(args []string, stdout, stderr io.Writer) error {
	opts, err := parseVerifyAssetsOptions(args, stderr)
	if err != nil {
		return err
	}
	if opts.version == "" {
		return fmt.Errorf("%w: 必须显式指定 -version", errUsage)
	}
	if opts.assetsFile == "" {
		return fmt.Errorf("%w: 必须显式指定 -assets-file", errUsage)
	}
	manifest, err := release.VerifyRelease(release.ReleaseVerifyOptions{
		Dir:     opts.dir,
		Version: opts.version,
	})
	if err != nil {
		return err
	}
	data, err := os.ReadFile(opts.assetsFile)
	if err != nil {
		return fmt.Errorf("读取资产列表失败: %w", err)
	}
	assets := parseAssetNames(string(data))
	if err := release.VerifyReleaseAssets(manifest, assets); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Release 资产核验通过: %d 个资产与发布清单一致\n", len(assets))
	return nil
}

// parseAssetNames 解析资产名列表：每行一个名称，忽略空行与首尾空白。
func parseAssetNames(data string) []string {
	names := make([]string, 0)
	for _, line := range strings.Split(data, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
