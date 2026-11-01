package main

import (
	"fmt"
	"io"
	"os"

	"github.com/lingting/agentflow/internal/release"
)

// writeReleaseNotes 由发布清单生成 Release 版本说明。
//
// 说明只在"清单已完整声明来源信息"时生成：正式 Release 的正文必须能指向来源证明，
// 不能用一份没有来源信息的说明充当发布记录。
func writeReleaseNotes(args []string, stdout, stderr io.Writer) error {
	opts, err := parseNotesOptions(args, stderr)
	if err != nil {
		return err
	}
	if opts.version == "" {
		return fmt.Errorf("%w: 必须显式指定 -version", errUsage)
	}
	manifest, err := release.VerifyRelease(release.ReleaseVerifyOptions{
		Dir:               opts.dir,
		Version:           opts.version,
		RequireProvenance: true,
	})
	if err != nil {
		return err
	}
	notes := manifest.Notes()
	if opts.output == "" {
		if _, err := io.WriteString(stdout, notes); err != nil {
			return fmt.Errorf("写出发布说明失败: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(opts.output, []byte(notes), 0o644); err != nil {
		return fmt.Errorf("写出发布说明失败: %w", err)
	}
	fmt.Fprintf(stdout, "发布说明已写入 %s\n", opts.output)
	return nil
}
