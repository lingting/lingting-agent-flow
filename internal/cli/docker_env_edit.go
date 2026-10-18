package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// defaultEditor 是未配置编辑器时使用的本机编辑器。
const defaultEditor = "vi"

// editTempPrefix 是本机编辑临时目录的前缀。
const editTempPrefix = "agentflow-env-edit-"

// newDockerEnvEditCommand 构建 docker env edit 子命令。
//
// 编辑流程：读取远程当前版本 → 写入本机独立临时目录 → 用用户编辑器编辑 →
// 校验内容 → 通过 SFTP 保存为新的远程版本。取消编辑或保存失败都不会修改
// 当前生效版本，也不会覆盖任何旧文件。
func newDockerEnvEditCommand(a *app.App) *cobra.Command {
	var flags envFlags
	var flagsCredentials credentialFlags
	var editor string
	var parentVersion string
	var nonInteractive bool
	var stdinContent bool
	command := &cobra.Command{
		Use:   "edit",
		Short: "编辑环境 Dockerfile 并保存为新版本",
		Long: "从远程读取指定版本（默认最新版本）的 Dockerfile，放入本机独立临时目录，\n" +
			"用 --editor 或 $EDITOR 指定的编辑器打开；保存后校验内容并生成新的远程版本。\n" +
			"本机临时目录不是配置的权威存储位置，正式版本仍保存在远程工作空间。\n" +
			"未修改内容时不会创建新版本；取消编辑不会改变任何远程文件。\n" +
			"--stdin 从标准输入读取内容，用于非交互场景。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := flags.resolve()
			if err != nil {
				return err
			}
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				provider, providerErr := credentialProviderForArgs(
					ctx,
					service.SSHService(),
					flags.Machine,
					flagsCredentials,
				)
				if providerErr != nil {
					return providerErr
				}
				current, readErr := service.ReadDockerfile(ctx, app.ReadDockerfileRequest{
					MachineID:     flags.Machine,
					Ref:           ref,
					VersionNumber: flags.Version,
					Credentials:   provider,
				})
				if readErr != nil {
					return readErr
				}
				edited, editErr := readEditedDockerfile(a, current.Content, editOptions{
					Editor:         editor,
					NonInteractive: nonInteractive,
					FromStdin:      stdinContent,
				})
				if editErr != nil {
					return editErr
				}
				if string(edited) == string(current.Content) {
					_, err := fmt.Fprintf(
						a.Stdout(),
						"内容未修改，未创建新版本（当前版本 %s）。\n",
						current.Version.Label(),
					)
					return err
				}
				parent := strings.TrimSpace(parentVersion)
				if parent == "" && !ref.IsBase() {
					parent = current.Version.ParentVersion
				}
				version, createErr := service.CreateVersion(ctx, app.CreateVersionRequest{
					MachineID:     flags.Machine,
					Ref:           ref,
					Dockerfile:    edited,
					ParentVersion: parent,
					Credentials:   provider,
				})
				if createErr != nil {
					return createErr
				}
				_, err = fmt.Fprintf(
					a.Stdout(),
					"已保存新版本: %s\n远程版本目录: %s\n来源版本: %s\n下一步: "+
						"agentflow docker env build --machine %s --type %s --name %s --version %d\n",
					version.Label(),
					remoteVersionPath(version),
					current.Version.Label(),
					flags.Machine,
					version.Ref.Type,
					version.Ref.Name,
					version.Number,
				)
				return err
			})
		},
	}
	addEnvFlags(command, &flags, true)
	addCredentialFlags(&flagsCredentials, command)
	command.Flags().StringVar(
		&editor,
		"editor",
		"",
		"本机编辑器命令，省略时使用 $EDITOR 或 "+defaultEditor,
	)
	command.Flags().StringVar(
		&parentVersion,
		"parent-version",
		"",
		"语言环境依赖的父基础环境版本标签，省略时沿用来源版本",
	)
	command.Flags().BoolVar(
		&nonInteractive,
		"no-editor",
		false,
		"不打开编辑器，只把内容写入本机临时文件并输出路径",
	)
	command.Flags().BoolVar(&stdinContent, "stdin", false, "从标准输入读取 Dockerfile 内容")
	return command
}

// editOptions 约束一次本机编辑。
type editOptions struct {
	// Editor 是编辑器命令，空值使用 $EDITOR 或默认值。
	Editor string
	// NonInteractive 表示不打开编辑器。
	NonInteractive bool
	// FromStdin 表示从标准输入读取内容。
	FromStdin bool
}

// readEditedDockerfile 把内容放入本机临时目录并返回用户编辑后的结果。
//
// 临时目录使用随机名称并位于系统临时目录下，不作为长期配置存储位置；
// 编辑失败或取消时不会产生新的远程版本。
func readEditedDockerfile(
	a *app.App,
	content []byte,
	options editOptions,
) ([]byte, error) {
	if options.FromStdin {
		data, err := readAllStdin()
		if err != nil {
			return nil, err
		}
		if err := docker.ValidateDockerfile(data); err != nil {
			return nil, err
		}
		return data, nil
	}
	directory, err := os.MkdirTemp("", editTempPrefix)
	if err != nil {
		return nil, fmt.Errorf("创建本机编辑临时目录失败: %w", err)
	}
	target := filepath.Join(directory, docker.DockerfileName)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return nil, fmt.Errorf("写入本机编辑临时文件失败: %s: %w", target, err)
	}
	if _, err := fmt.Fprintf(
		a.Stdout(),
		"本机编辑文件: %s\n（该目录只是临时编辑位置，正式版本保存在远程工作空间）\n",
		target,
	); err != nil {
		return nil, err
	}
	if options.NonInteractive {
		if _, err := fmt.Fprintf(
			a.Stdout(),
			"未打开编辑器；请编辑上述文件后使用 --stdin 或 --editor 重新执行。\n",
		); err != nil {
			return nil, err
		}
		return content, nil
	}
	if err := runEditor(options.Editor, target); err != nil {
		return nil, err
	}
	edited, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("读取编辑结果失败: %s: %w", target, err)
	}
	if err := docker.ValidateDockerfile(edited); err != nil {
		return nil, fmt.Errorf("编辑结果校验失败，未保存任何远程版本: %w", err)
	}
	return edited, nil
}

// runEditor 用本机编辑器打开文件并等待其结束。
func runEditor(configured, target string) error {
	name, arguments := resolveEditor(configured)
	command := exec.Command(name, append(arguments, target)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("编辑器 %s 执行失败: %w", name, err)
	}
	return nil
}

// resolveEditor 解析编辑器命令及其固定参数。
func resolveEditor(configured string) (string, []string) {
	raw := strings.TrimSpace(configured)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if raw == "" {
		return defaultEditor, nil
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return defaultEditor, nil
	}
	return fields[0], fields[1:]
}

// readAllStdin 从标准输入读取全部内容。
func readAllStdin() ([]byte, error) {
	data, err := io.ReadAll(osStdin())
	if err != nil {
		return nil, fmt.Errorf("读取标准输入失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("标准输入为空，未保存任何远程版本")
	}
	return data, nil
}

// remoteVersionPath 返回版本目录的远程路径表示。
//
// 命令层不持有连接，因此这里只输出相对工作空间的路径片段，具体根路径由
// docker env show 输出。
func remoteVersionPath(version domain.EnvironmentVersion) string {
	segments := []string{docker.DirectoryEnvironments}
	if version.Ref.IsBase() {
		segments = append(segments, version.Ref.Name)
	} else {
		segments = append(segments, "languages", version.Ref.Name)
	}
	segments = append(segments, docker.VersionDirectoryName, version.Label())
	return "<工作空间>/" + strings.Join(segments, "/")
}
