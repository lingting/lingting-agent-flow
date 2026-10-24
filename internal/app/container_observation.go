package app

import (
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// ClassifyContainerObservation 把一次容器核验的结果归类为可稳定判断的观测结论。
//
// 恢复必须区分"确认不存在"与"无法访问"：前者可以收敛为已删除，后者只能保留
// 不确定性。仓库中的 docker.ErrorCodeOf 已经提供了稳定类别，因此这里只做映射，
// 不解析错误文本，也不把不同原因压平成同一个布尔值。
//
// 返回的 DockerID 只在"登记记录缺少 docker_id、但按名称核验成功"时给出：此时缺 ID
// 不是孤儿，而是"创建成功但 ID 没落库"，补写 ID 才是正确的收敛。
func ClassifyContainerObservation(
	container domain.Container,
	detail ContainerDetail,
	cause error,
) (domain.ContainerObservationClass, string) {
	if cause != nil {
		switch docker.ErrorCodeOf(cause) {
		case docker.CodeContainerNotFound:
			return domain.ContainerObservedAbsent, ""
		case docker.CodeOwnershipMismatch:
			// 同名容器存在但标签不匹配：它不是"不存在"，也不能被自动处置。
			return domain.ContainerObservedOwnershipMismatch, ""
		case docker.CodeDetached:
			// 已脱离管理的容器由 DecideContainerRecovery 统一处理，
			// 观测结论在这里按"无法自动处置"归类。
			return domain.ContainerObservedUnreachable, ""
		case docker.CodeStateUncertain, docker.CodeSSHUnavailable,
			docker.CodeCLIMissing, docker.CodeDaemonUnavailable,
			docker.CodePermissionDenied, docker.CodeVersionUnsupported:
			return domain.ContainerObservedUnreachable, ""
		default:
			// 未分类错误一律按"无法确认"处理：把它当"确认不存在"会误伤真实容器。
			return domain.ContainerObservedUnreachable, ""
		}
	}
	if detail.Inspect == nil {
		// 没有远程观测结果说明本次没有真正看到容器：缺 ID 且按名称也无法核验时，
		// 只能报告"记录不完整"，不能假定它存在或不存在。
		if strings.TrimSpace(container.DockerID) == "" {
			return domain.ContainerObservedIncomplete, ""
		}
		return domain.ContainerObservedUnreachable, ""
	}
	dockerID := strings.TrimSpace(detail.Inspect.ID)
	if strings.TrimSpace(container.DockerID) == "" && dockerID != "" {
		// 按名称核验到了真实容器：这是"ID 没落库"，不是"容器不存在"。
		if detail.Inspect.Running {
			return domain.ContainerObservedRunning, dockerID
		}
		return domain.ContainerObservedStopped, dockerID
	}
	if detail.Inspect.Running {
		return domain.ContainerObservedRunning, ""
	}
	return domain.ContainerObservedStopped, ""
}

// ErrContainerObservationUnavailable 表示容器观测没有完成。
//
// 恢复用它区分"容器不存在"与"无法访问"：后者不允许任何破坏性处置，也不需要
// 让启动失败——现场保持 unknown，等待下一次可访问时重新收敛。
var ErrContainerObservationUnavailable = errors.New("app: 容器状态无法确认")
