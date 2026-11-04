package app

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// 规划角色 Session 的固定布局。
const (
	// planningArchiveDirName 是本机规划 Session 归档的顶层目录名。
	//
	// 该目录位于配置的本机 Session 根目录之下，不写入项目代码目录、仓库目录或远程
	// 配置目录。
	planningArchiveDirName = "planning"
	// planningSessionFileName 是稳定 Session 文件名。
	planningSessionFileName = "session.jsonl"
)

// planningAgentLayout 是规划角色稳定 Session 的固定位置。
type planningAgentLayout struct {
	// containerSession 是容器内固定的 Session 文件路径（POSIX 形式）。
	containerSession string
	// archiveDir 是本机持久归档目录的绝对路径。
	archiveDir string
	// archiveFile 是本机可恢复快照文件的绝对路径。
	archiveFile string
}

// planningAgentSessionLayout 生成规划角色的稳定 Session 位置。
//
// 容器内路径使用 POSIX 形式，位于规划容器的 Session 根目录下，并按规划会话标识与角色
// 会话标识分层：两个角色互相隔离，不同规划会话之间互相隔离，同一角色的多轮调用共用
// 同一个文件。本机归档位于 Session 根目录的 planning/<规划会话标识>/<角色会话标识>/。
// 路径全部由服务端生成，调用方无法提交任意路径；根目录包含关系由 Runner 在落盘前再
// 校验一次。
func planningAgentSessionLayout(
	sessionRoot, planningSessionID, roleSessionID string,
) (planningAgentLayout, error) {
	root := strings.TrimSpace(sessionRoot)
	if root == "" {
		return planningAgentLayout{}, fmt.Errorf(
			"%w: 本机 Session 根目录不能为空",
			ErrPlanningAgentInvalidInput,
		)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return planningAgentLayout{}, fmt.Errorf("解析本机 Session 根目录失败: %w", err)
	}
	containerPath, err := planningRoleContainerSessionPath(planningSessionID, roleSessionID)
	if err != nil {
		return planningAgentLayout{}, err
	}
	archiveDir, err := planningRoleArchiveDir(absoluteRoot, planningSessionID, roleSessionID)
	if err != nil {
		return planningAgentLayout{}, err
	}
	return planningAgentLayout{
		containerSession: containerPath,
		archiveDir:       archiveDir,
		archiveFile:      filepath.Join(archiveDir, planningSessionFileName),
	}, nil
}

// planningRoleContainerSessionPath 生成容器内的稳定 Session 文件路径。
func planningRoleContainerSessionPath(
	planningSessionID, roleSessionID string,
) (string, error) {
	planningDir, err := planningPathSegment(planningSessionID, "规划会话标识")
	if err != nil {
		return "", err
	}
	roleDir, err := planningPathSegment(roleSessionID, "角色会话标识")
	if err != nil {
		return "", err
	}
	return path.Join(planningSessionDir, planningDir, roleDir, planningSessionFileName), nil
}

// planningRoleArchiveDir 生成本机归档目录的绝对路径。
func planningRoleArchiveDir(
	absoluteRoot, planningSessionID, roleSessionID string,
) (string, error) {
	planningDir, err := planningPathSegment(planningSessionID, "规划会话标识")
	if err != nil {
		return "", err
	}
	roleDir, err := planningPathSegment(roleSessionID, "角色会话标识")
	if err != nil {
		return "", err
	}
	return filepath.Join(absoluteRoot, planningArchiveDirName, planningDir, roleDir), nil
}

// planningPathSegment 校验标识可以直接作为一层目录名。
//
// 标识来自数据库记录，而路径必须结构化：只接受字母、数字、连字符、下划线与点号，
// 拒绝空片段与 "." / ".."，避免标识内容改变目录层级。
func planningPathSegment(raw, field string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: %s 不能为空", ErrPlanningAgentInvalidInput, field)
	}
	if value == "." || value == ".." {
		return "", fmt.Errorf("%w: %s 不能是 %q", ErrPlanningAgentInvalidInput, field, value)
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.':
		default:
			return "", fmt.Errorf(
				"%w: %s 含有不能用于目录名的字符 %q",
				ErrPlanningAgentInvalidInput,
				field,
				char,
			)
		}
	}
	return value, nil
}
