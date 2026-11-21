package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// runPrepareRealConfig 是 CI 的真实测试配置准备入口：按 real-tests 环境变量生成
// tests/config/*.yml 与临时凭据文件后退出，不执行任何用例（V3 R8）。
//
// 退出码与执行器其余模式一致：0 = 已生成配置或按缺少凭据跳过，1 = 配置无法生成。
func runPrepareRealConfig(stdout, stderr io.Writer) int {
	input := realConfigInputFromEnv()
	// 先打印掩码：之后任何输出里出现的凭据值都会被工作流遮住。
	masked := testrunner.MaskedSecretLines(
		input.SSHPrivateKey,
		input.SSHKnownHosts,
		input.DockerPIAPIKey,
		input.GitSSHKey,
	)
	for _, line := range masked {
		fmt.Fprintf(stdout, "::add-mask::%s\n", line)
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "错误: 无法确定当前目录: %v\n", err)
		return testrunner.ExitFailure
	}
	result, err := testrunner.PrepareRealConfig(repoRoot, input)
	if err != nil {
		fmt.Fprintf(stderr, "::error::真实测试配置准备失败: %v\n", err)
		return testrunner.ExitFailure
	}
	if err := writeRealConfigOutputs(result); err != nil {
		fmt.Fprintf(stderr, "::error::写出配置准备结论失败: %v\n", err)
		return testrunner.ExitFailure
	}
	if !result.Configured {
		fmt.Fprintf(stdout, "SKIP：real-tests 环境缺少必需 Secret: %s\n", strings.Join(result.MissingSecrets, ", "))
		return testrunner.ExitOK
	}
	fmt.Fprintf(stdout, "已生成真实测试配置: %s\n", strings.Join(result.Written, ", "))
	return testrunner.ExitOK
}

// realConfigInputFromEnv 从环境变量读取配置输入。
//
// 连接目标与凭据来自 real-tests 环境的 Environment Secrets；REAL_PI_MODEL 是可选套件参数，
// 不参与授权判定。
func realConfigInputFromEnv() testrunner.RealConfigInput {
	return testrunner.RealConfigInput{
		SSHHost:               os.Getenv("REAL_SSH_HOST"),
		SSHUser:               os.Getenv("REAL_SSH_USER"),
		SSHPort:               os.Getenv("REAL_SSH_PORT"),
		SSHPrivateKey:         os.Getenv("REAL_SSH_PRIVATE_KEY"),
		SSHKnownHosts:         os.Getenv("REAL_SSH_KNOWN_HOSTS"),
		DockerAgentImage:      os.Getenv("REAL_DOCKER_AGENT_IMAGE"),
		DockerGitImage:        os.Getenv("REAL_DOCKER_GIT_IMAGE"),
		DockerPIBaseURL:       os.Getenv("REAL_DOCKER_PI_BASE_URL"),
		DockerPIAPIKey:        os.Getenv("REAL_DOCKER_PI_API_KEY"),
		DockerPIModel:         os.Getenv("REAL_DOCKER_PI_MODEL"),
		DockerPIThinkingLevel: os.Getenv("REAL_DOCKER_PI_THINKING_LEVEL"),
		GitRepository:         os.Getenv("REAL_GIT_REPOSITORY"),
		GitSSHKey:             os.Getenv("REAL_GIT_SSH_KEY"),
		PIModel:               os.Getenv("REAL_PI_MODEL"),
		WorkRoot:              os.Getenv("RUNNER_TEMP"),
	}
}

// writeRealConfigOutputs 把结论写进 GitHub Actions 的输出文件与 Job 摘要。
//
// 两个环境变量都只在 CI 中存在；本地运行时什么都不写，结论以标准输出与退出码表达。
func writeRealConfigOutputs(result testrunner.RealConfigResult) error {
	configured := "false"
	if result.Configured {
		configured = "true"
	}
	if path := strings.TrimSpace(os.Getenv("GITHUB_OUTPUT")); path != "" {
		if err := appendToFile(path, "configured="+configured+"\n"); err != nil {
			return fmt.Errorf("写入 GITHUB_OUTPUT 失败: %w", err)
		}
	}
	if path := strings.TrimSpace(os.Getenv("GITHUB_STEP_SUMMARY")); path != "" {
		if err := appendToFile(path, result.Summary); err != nil {
			return fmt.Errorf("写入 GITHUB_STEP_SUMMARY 失败: %w", err)
		}
	}
	return nil
}

// appendToFile 以追加方式写文件，不存在时创建。
func appendToFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
