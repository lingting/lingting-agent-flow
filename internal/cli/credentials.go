package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ErrInteractiveUnavailable 表示当前运行环境不支持交互式凭据输入。
var ErrInteractiveUnavailable = errors.New("cli: 当前环境不支持交互式输入")

// credentialFlags 是凭据输入相关的命令行参数。
type credentialFlags struct {
	// PasswordStdin 表示从标准输入读取一行密码。
	PasswordStdin bool
	// PassphraseStdin 表示从标准输入读取一行私钥口令。
	PassphraseStdin bool
	// AskPass 强制使用隐藏交互输入。
	AskPass bool
}

// credentialOptions 是构造凭据提供器所需的运行期依赖。
type credentialOptions struct {
	// Method 是节点配置的认证方式。
	Method sshclient.AuthMethod
	// Flags 是命令行给出的凭据输入方式。
	Flags credentialFlags
	// Stdin 是标准输入，默认 os.Stdin。
	Stdin io.Reader
	// Prompt 是提示信息的输出目标，默认 os.Stderr。
	Prompt io.Writer
	// IsTerminal 判断输入是否为终端，默认检测 os.Stdin。
	IsTerminal func() bool
	// ReadPassword 从终端读取隐藏输入，默认使用 golang.org/x/term。
	ReadPassword func() ([]byte, error)
}

// newCredentialProvider 按认证方式与命令行参数构造凭据提供器。
//
// 三种认证方式的行为：
//   - SSH Agent 不需要凭据，直接返回 nil；
//   - 密码与私钥口令需要凭据，可显式从标准输入读取，或使用隐藏交互输入；
//   - 非交互环境没有可用凭据时返回明确错误，不会挂起等待输入。
func newCredentialProvider(options credentialOptions) (sshclient.CredentialProvider, error) {
	switch options.Method {
	case sshclient.AuthMethodAgent:
		return nil, nil
	case sshclient.AuthMethodPassword:
		return newPasswordProvider(options)
	case sshclient.AuthMethodPrivateKey:
		return newPassphraseProvider(options)
	default:
		return nil, fmt.Errorf("不支持的认证方式 %q", options.Method)
	}
}

// newPasswordProvider 构造密码提供器。
func newPasswordProvider(options credentialOptions) (sshclient.CredentialProvider, error) {
	secret, err := readSecret(options, "密码", options.Flags.PasswordStdin)
	if err != nil {
		return nil, err
	}
	return sshclient.PasswordProvider{Password: secret}, nil
}

// newPassphraseProvider 构造私钥口令提供器。
//
// 口令是可选的：未加密的私钥不需要口令，因此只有在调用方显式要求读取口令
// （--passphrase-stdin 或 --ask-pass）时才提示输入，避免非交互环境被无谓地拦下。
func newPassphraseProvider(options credentialOptions) (sshclient.CredentialProvider, error) {
	if !options.Flags.PassphraseStdin && !options.Flags.AskPass {
		return nil, nil
	}
	secret, err := readSecret(options, "私钥口令", options.Flags.PassphraseStdin)
	if err != nil {
		return nil, err
	}
	return sshclient.PassphraseProvider{Passphrase: secret}, nil
}

// readSecret 读取一次凭据输入。
//
// fromStdin 为 true 时读取标准输入的一行；否则在终端可用时使用隐藏输入。
// 两种方式都不可用时返回明确错误，绝不阻塞等待。
func readSecret(options credentialOptions, label string, fromStdin bool) ([]byte, error) {
	if fromStdin {
		return readSecretFromStdin(options, label)
	}
	if options.Flags.AskPass {
		return readSecretFromTerminal(options, label)
	}
	if options.IsTerminal == nil || !options.IsTerminal() {
		return nil, fmt.Errorf(
			"%w: 非交互环境无法输入%s，请使用 %s 从标准输入提供，"+
				"或在交互式终端中使用 %s",
			sshclient.ErrNoCredential,
			label,
			secretStdinFlag(label),
			secretAskFlag(label),
		)
	}
	return readSecretFromTerminal(options, label)
}

// readSecretFromStdin 从标准输入读取一行凭据。
func readSecretFromStdin(options credentialOptions, label string) ([]byte, error) {
	source := options.Stdin
	if source == nil {
		source = os.Stdin
	}
	reader := bufio.NewReader(source)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("读取%s失败: %w", label, err)
	}
	secret := strings.TrimRight(line, "\r\n")
	if secret == "" {
		return nil, fmt.Errorf("%w: 标准输入没有提供%s", sshclient.ErrNoCredential, label)
	}
	return []byte(secret), nil
}

// readSecretFromTerminal 使用隐藏输入读取一次凭据。
func readSecretFromTerminal(options credentialOptions, label string) ([]byte, error) {
	prompt := options.Prompt
	if prompt == nil {
		prompt = os.Stderr
	}
	if _, err := fmt.Fprintf(prompt, "请输入 %s: ", label); err != nil {
		return nil, fmt.Errorf("输出提示信息失败: %w", err)
	}
	readPassword := options.ReadPassword
	if readPassword == nil {
		readPassword = defaultReadPassword
	}
	secret, err := readPassword()
	// 隐藏输入不会回显换行，这里补一个换行，避免后续输出粘在同一行。
	_, _ = fmt.Fprintln(prompt)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: %s输入被中断", sshclient.ErrCredentialCancelled, label)
		}
		return nil, fmt.Errorf("读取%s失败: %w", label, err)
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("%w: 没有输入%s", sshclient.ErrNoCredential, label)
	}
	return secret, nil
}

// defaultReadPassword 使用终端隐藏输入读取一行。
func defaultReadPassword() ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, ErrInteractiveUnavailable
	}
	return term.ReadPassword(fd)
}

// defaultIsTerminal 判断标准输入是否为终端。
func defaultIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// secretStdinFlag 返回读取凭据的标准输入参数名。
func secretStdinFlag(label string) string {
	if label == "私钥口令" {
		return "--passphrase-stdin"
	}
	return "--password-stdin"
}

// osStdin 返回标准输入。
func osStdin() io.Reader {
	return os.Stdin
}

// osStderr 返回标准错误流，用于输出凭据提示。
func osStderr() io.Writer {
	return os.Stderr
}

// secretAskFlag 返回强制交互输入的参数名。
func secretAskFlag(label string) string {
	if label == "私钥口令" {
		return "--ask-pass（同时输入私钥口令）"
	}
	return "--ask-pass"
}

// addCredentialFlags 为命令注册凭据输入参数。
func addCredentialFlags(flags *credentialFlags, command *cobra.Command) {
	command.Flags().BoolVar(
		&flags.PasswordStdin,
		"password-stdin",
		false,
		"从标准输入读取一行 SSH 密码（不落盘、不回显）",
	)
	command.Flags().BoolVar(
		&flags.PassphraseStdin,
		"passphrase-stdin",
		false,
		"从标准输入读取一行私钥口令（不落盘、不回显）",
	)
	command.Flags().BoolVar(
		&flags.AskPass,
		"ask-pass",
		false,
		"强制使用隐藏交互输入密码或私钥口令",
	)
}
