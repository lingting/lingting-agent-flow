package resourcecheck

import (
	"os"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// PiCLICase 验证 pi-cli 提供者：句柄给出真实可执行文件、版本与内容指纹。
//
// 提供者只用 --version 探测可执行文件，因此本用例不再发起任何真实 Pi 调用：句柄里的版本
// 与指纹就是分配阶段的探测结果。
var PiCLICase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/pi-cli",
	Timeout: 60 * time.Second,
	Resources: []testrunner.ResourceDecl{{
		Name: "pi", Type: testrunner.ResourcePiCLI, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("pi")
		if !ok {
			c.Fatalf("缺少资源 pi 的句柄")
		}
		path := handle.PayloadValue("path")
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			c.Fatalf("Pi CLI %s 不可用: %v", path, err)
		}
		version := handle.PayloadValue("version")
		if version == "" {
			c.Fatalf("句柄缺少版本号")
		}
		if handle.Fingerprint == "" {
			c.Fatalf("句柄缺少内容指纹")
		}
		c.Logf("Pi CLI 资源可用：%s %s（指纹 %s）", path, version, handle.Fingerprint)
	},
})
