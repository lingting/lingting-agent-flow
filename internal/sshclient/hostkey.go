package sshclient

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyInfo 描述一台远程主机的主机密钥，用于向用户展示并核验指纹。
type HostKeyInfo struct {
	// Host 是主机地址，不含端口。
	Host string
	// Port 是 SSH 端口。
	Port int
	// Algorithm 是主机密钥算法，例如 ssh-ed25519。
	Algorithm string
	// Fingerprint 是 SHA-256 指纹，形如 SHA256:xxxx。
	Fingerprint string

	// key 是原始公钥，只在持久化信任记录时使用。
	key ssh.PublicKey
}

// Address 返回 host:port 形式的地址，IPv6 会自动加方括号。
func (i HostKeyInfo) Address() string {
	return net.JoinHostPort(i.Host, strconv.Itoa(i.Port))
}

// String 返回便于用户核验的单行描述。
func (i HostKeyInfo) String() string {
	return fmt.Sprintf("%s %s %s", i.Address(), i.Algorithm, i.Fingerprint)
}

// PublicKey 返回主机公钥，未设置时返回 nil。
func (i HostKeyInfo) PublicKey() ssh.PublicKey {
	return i.key
}

// newHostKeyInfo 由公钥构造主机密钥信息。
func newHostKeyInfo(host string, port int, key ssh.PublicKey) HostKeyInfo {
	if key == nil {
		return HostKeyInfo{Host: host, Port: port}
	}
	return HostKeyInfo{
		Host:        host,
		Port:        port,
		Algorithm:   key.Type(),
		Fingerprint: ssh.FingerprintSHA256(key),
		key:         key,
	}
}

// HostKeyVerdict 是主机密钥的校验结论。
type HostKeyVerdict string

// 主机密钥校验结论取值。
const (
	// VerdictTrusted 表示主机密钥与已信任记录一致。
	VerdictTrusted HostKeyVerdict = "trusted"
	// VerdictUnknown 表示该主机没有任何已信任的主机密钥。
	VerdictUnknown HostKeyVerdict = "unknown"
	// VerdictMismatch 表示主机密钥与已信任记录不一致。
	VerdictMismatch HostKeyVerdict = "mismatch"
	// VerdictRevoked 表示主机密钥已被标记为吊销。
	VerdictRevoked HostKeyVerdict = "revoked"
)

// String 返回校验结论的字符串形式。
func (v HostKeyVerdict) String() string {
	return string(v)
}

// HostKeyCheck 是一次主机密钥校验的完整结果。
type HostKeyCheck struct {
	// Verdict 是校验结论。
	Verdict HostKeyVerdict
	// Presented 是服务端实际提供的主机密钥信息。
	Presented HostKeyInfo
	// Trusted 是已信任记录中的主机密钥信息，仅在 VerdictMismatch 时非空。
	Trusted []HostKeyInfo
}

// HostKeyStore 负责主机密钥的校验与信任记录持久化。
//
// 实现方不得静默接受未知或发生变化的主机密钥：未知主机必须返回 VerdictUnknown，
// 变化的主机密钥必须返回 VerdictMismatch，由调用方决定是否让用户确认。
type HostKeyStore interface {
	// Check 校验服务端提供的主机密钥。
	Check(host string, port int, key ssh.PublicKey) (HostKeyCheck, error)
}

// KnownHostsOptions 是构造 KnownHostsStore 的参数。
type KnownHostsOptions struct {
	// TrustedPaths 是只读的 known_hosts 文件列表，通常是用户 OpenSSH 的 known_hosts。
	//
	// 这些文件只读取，不会被本包创建、追加或修改：信任记录由用户在 AgentFlow 之外
	// 维护（例如用 OpenSSH 把已核验的密钥追加进 known_hosts）。
	TrustedPaths []string
}

// KnownHostsStore 基于 OpenSSH known_hosts 格式实现 HostKeyStore。
//
// 本实现只读：不创建、不追加、不加锁、不写临时文件，因此并发校验不需要任何互斥，
// 也不会与用户自己编辑 known_hosts 的操作互相干扰。
type KnownHostsStore struct {
	trustedPaths []string
}

// NewKnownHostsStore 构造 known_hosts 主机密钥库。
//
// 至少需要一个 known_hosts 路径；路径在构造时统一转为绝对路径并去重。
func NewKnownHostsStore(options KnownHostsOptions) (*KnownHostsStore, error) {
	trusted := make([]string, 0, len(options.TrustedPaths))
	seen := make(map[string]struct{}, len(options.TrustedPaths))
	for _, path := range options.TrustedPaths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return nil, fmt.Errorf("sshclient: 解析主机密钥记录路径失败: %s: %w", trimmed, err)
		}
		if _, done := seen[absolute]; done {
			continue
		}
		seen[absolute] = struct{}{}
		trusted = append(trusted, absolute)
	}
	if len(trusted) == 0 {
		return nil, fmt.Errorf("sshclient: 至少需要一个 known_hosts 文件路径")
	}
	return &KnownHostsStore{trustedPaths: trusted}, nil
}

// TrustedPaths 返回参与校验的 known_hosts 文件绝对路径列表。
//
// 返回副本，调用方修改不会影响主机密钥库。
func (s *KnownHostsStore) TrustedPaths() []string {
	return append([]string{}, s.trustedPaths...)
}

// Check 校验服务端提供的主机密钥。
//
// 结论只可能是受信任、未知、不一致或吊销四种；未知与不一致都不会被静默接受。
func (s *KnownHostsStore) Check(host string, port int, key ssh.PublicKey) (HostKeyCheck, error) {
	presented := newHostKeyInfo(host, port, key)
	if key == nil {
		return HostKeyCheck{Verdict: VerdictUnknown, Presented: presented}, fmt.Errorf(
			"sshclient: 服务端未提供主机密钥",
		)
	}
	callback, err := knownhosts.New(s.existingPaths()...)
	if err != nil {
		return HostKeyCheck{Verdict: VerdictUnknown, Presented: presented}, fmt.Errorf(
			"sshclient: 读取主机密钥记录失败: %w",
			err,
		)
	}
	// 校验回调要求 address 是 host:port 形式：knownhosts.Normalize 在端口为 22 时
	// 会去掉端口，直接传入会导致其内部的 SplitHostPort 失败，因此这里传入未规范化
	// 的地址。
	address := presented.Address()
	checkErr := callback(address, &knownHostsAddr{address: address}, key)
	if checkErr == nil {
		return HostKeyCheck{Verdict: VerdictTrusted, Presented: presented}, nil
	}
	return s.classifyCheckError(checkErr, presented)
}

// classifyCheckError 把 knownhosts 的错误转换为校验结论。
func (s *KnownHostsStore) classifyCheckError(
	checkErr error,
	presented HostKeyInfo,
) (HostKeyCheck, error) {
	var revoked *knownhosts.RevokedError
	if errors.As(checkErr, &revoked) {
		return HostKeyCheck{Verdict: VerdictRevoked, Presented: presented}, fmt.Errorf(
			"sshclient: 主机密钥已被吊销: %s: %w",
			presented.String(),
			checkErr,
		)
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(checkErr, &keyErr) {
		return HostKeyCheck{Verdict: VerdictUnknown, Presented: presented}, fmt.Errorf(
			"sshclient: 校验主机密钥失败: %w",
			checkErr,
		)
	}
	if len(keyErr.Want) == 0 {
		return HostKeyCheck{Verdict: VerdictUnknown, Presented: presented}, nil
	}
	trusted := make([]HostKeyInfo, 0, len(keyErr.Want))
	for _, known := range keyErr.Want {
		trusted = append(trusted, newHostKeyInfo(presented.Host, presented.Port, known.Key))
	}
	return HostKeyCheck{
		Verdict:   VerdictMismatch,
		Presented: presented,
		Trusted:   trusted,
	}, nil
}

// existingPaths 返回当前存在的 known_hosts 文件列表。
func (s *KnownHostsStore) existingPaths() []string {
	paths := make([]string, 0, len(s.trustedPaths))
	for _, path := range s.trustedPaths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// knownHostsAddr 是 knownhosts 校验回调所需的远端地址。
//
// 校验只需要 host:port 文本，这里给出最小的 net.Addr 实现，
// 避免依赖真实的网络连接对象。
type knownHostsAddr struct {
	address string
}

// Network 返回网络类型。
func (a *knownHostsAddr) Network() string {
	return "tcp"
}

// String 返回 host:port 形式的地址。
func (a *knownHostsAddr) String() string {
	return a.address
}
