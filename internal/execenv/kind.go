package execenv

import "fmt"

// Kind 是执行环境的类型标识。
//
// 第一版只实现远程 Linux + Docker 容器。其余取值预留给本机执行环境，
// 在真正实现之前必须明确报告为不支持，不得静默回退到本机执行。
type Kind string

// 执行环境类型取值。
const (
	// KindRemoteContainer 表示通过 SSH 在远程 Linux 上运行的 Docker 容器。
	KindRemoteContainer Kind = "remote_container"
	// KindLocalLinux 表示本机 Linux 执行环境（尚未实现）。
	KindLocalLinux Kind = "local_linux"
	// KindLocalDarwin 表示本机 macOS 执行环境（尚未实现）。
	KindLocalDarwin Kind = "local_darwin"
	// KindLocalWindows 表示本机 Windows 执行环境（尚未实现）。
	KindLocalWindows Kind = "local_windows"
)

// String 返回执行环境类型的字符串形式。
func (k Kind) String() string {
	return string(k)
}

// Validate 校验执行环境类型是否属于已知取值。
func (k Kind) Validate() error {
	switch k {
	case KindRemoteContainer, KindLocalLinux, KindLocalDarwin, KindLocalWindows:
		return nil
	default:
		return fmt.Errorf("%w: 未知的执行环境类型 %q", ErrInvalidValue, string(k))
	}
}

// Supported 判断执行环境类型是否已经实现。
func (k Kind) Supported() bool {
	return k == KindRemoteContainer
}

// RequireSupported 在类型尚未实现时返回明确错误。
//
// 该检查是"不得静默回退到宿主机执行"的落点：未实现的环境必须报错，
// 而不是换一个环境继续执行。
func (k Kind) RequireSupported() error {
	if err := k.Validate(); err != nil {
		return err
	}
	if !k.Supported() {
		return fmt.Errorf(
			"%w: 执行环境 %s 尚未实现，当前只支持 %s",
			ErrUnsupportedKind,
			k,
			KindRemoteContainer,
		)
	}
	return nil
}
