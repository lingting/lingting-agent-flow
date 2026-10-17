package sshclient

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// Trust 持久化一条已确认信任的主机密钥记录。
	Trust(info HostKeyInfo) error
}

// KnownHostsOptions 是构造 KnownHostsStore 的参数。
type KnownHostsOptions struct {
	// TrustedPaths 是只读的 known_hosts 文件列表，通常是用户 OpenSSH 的 known_hosts。
	//
	// 这些文件只读取不写入，避免破坏用户既有的信任配置。
	TrustedPaths []string
	// StorePath 是 AgentFlow 自己的 known_hosts 文件路径，用于写入新的信任记录。
	//
	// 该文件同时参与校验，因此同一个主机既可以在用户 known_hosts 中信任，
	// 也可以在 AgentFlow 的记录中信任。
	StorePath string
	// LockTimeout 是等待信任记录文件写锁的最长时间，零值使用 DefaultLockTimeout。
	LockTimeout time.Duration
}

// DefaultLockTimeout 是等待信任记录文件写锁的默认时长。
const DefaultLockTimeout = 5 * time.Second

// KnownHostsStore 基于 OpenSSH known_hosts 格式实现 HostKeyStore。
//
// 读取时会合并用户既有的 known_hosts 与 AgentFlow 自己的记录文件；
// 写入只发生在 AgentFlow 自己的记录文件上，并通过独占锁文件串行化，
// 避免并发连接同时写文件破坏记录。
type KnownHostsStore struct {
	trustedPaths []string
	storePath    string
	lockTimeout  time.Duration
}

// NewKnownHostsStore 构造 known_hosts 主机密钥库。
func NewKnownHostsStore(options KnownHostsOptions) (*KnownHostsStore, error) {
	storePath := strings.TrimSpace(options.StorePath)
	if storePath == "" {
		return nil, fmt.Errorf("sshclient: 主机密钥记录文件路径不能为空")
	}
	trusted := make([]string, 0, len(options.TrustedPaths)+1)
	seen := make(map[string]struct{}, len(options.TrustedPaths)+1)
	for _, path := range append(append([]string{}, options.TrustedPaths...), storePath) {
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
	lockTimeout := options.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = DefaultLockTimeout
	}
	return &KnownHostsStore{
		trustedPaths: trusted,
		storePath:    trusted[len(trusted)-1],
		lockTimeout:  lockTimeout,
	}, nil
}

// StorePath 返回 AgentFlow 信任记录的写入路径。
func (s *KnownHostsStore) StorePath() string {
	return s.storePath
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
	// 的地址；只有写入记录时才使用 Normalize 得到 known_hosts 的书写形式。
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

// Trust 持久化一条已确认信任的主机密钥记录。
//
// 写入前会重新读取现有记录，已存在相同记录时直接返回，避免重复追加；
// 写入使用独占锁文件串行化，并通过临时文件加原子重命名保证不会留下半行记录。
func (s *KnownHostsStore) Trust(info HostKeyInfo) error {
	if info.key == nil {
		return fmt.Errorf("sshclient: 缺少主机公钥，无法记录信任")
	}
	if strings.TrimSpace(info.Host) == "" {
		return fmt.Errorf("sshclient: 缺少主机地址，无法记录信任")
	}
	release, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer release()
	existing, err := os.ReadFile(s.storePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sshclient: 读取主机密钥记录失败: %s: %w", s.storePath, err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(info.Address())}, info.key)
	if containsKnownHostsLine(string(existing), line) {
		return nil
	}
	content := appendKnownHostsLine(existing, line)
	return s.writeAtomically(content)
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

// acquireLock 取得信任记录文件的独占写锁，返回释放函数。
//
// 锁通过独占创建的锁文件实现，跨进程有效且不依赖平台相关的文件锁接口；
// 持有者异常退出时残留的锁文件会在超时后被接管。
func (s *KnownHostsStore) acquireLock() (func(), error) {
	lockPath := s.storePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(s.storePath), 0o700); err != nil {
		return nil, fmt.Errorf("sshclient: 创建主机密钥记录目录失败: %w", err)
	}
	deadline := time.Now().Add(s.lockTimeout)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			return func() {
				_ = os.Remove(lockPath)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("sshclient: 获取主机密钥记录写锁失败: %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			// 超时后清理残留锁，避免持有者崩溃导致后续写入永久失败。
			_ = os.Remove(lockPath)
			return nil, fmt.Errorf(
				"sshclient: 等待主机密钥记录写锁超时: %s（如无其他进程正在连接，可删除该锁文件）",
				lockPath,
			)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// writeAtomically 通过临时文件加原子重命名写入信任记录。
func (s *KnownHostsStore) writeAtomically(content []byte) error {
	directory := filepath.Dir(s.storePath)
	temp, err := os.CreateTemp(directory, filepath.Base(s.storePath)+".tmp*")
	if err != nil {
		return fmt.Errorf("sshclient: 创建临时信任记录文件失败: %w", err)
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sshclient: 写入信任记录失败: %s: %w", s.storePath, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sshclient: 同步信任记录失败: %s: %w", s.storePath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("sshclient: 关闭临时信任记录失败: %s: %w", s.storePath, err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("sshclient: 设置信任记录权限失败: %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, s.storePath); err != nil {
		return fmt.Errorf("sshclient: 替换信任记录失败: %s: %w", s.storePath, err)
	}
	cleanup = false
	return nil
}

// containsKnownHostsLine 判断记录中是否已存在同一行。
func containsKnownHostsLine(content, line string) bool {
	for _, existing := range strings.Split(content, "\n") {
		if strings.TrimSpace(existing) == strings.TrimSpace(line) {
			return true
		}
	}
	return false
}

// appendKnownHostsLine 在内容末尾追加一行，保证原有行不被破坏。
func appendKnownHostsLine(content []byte, line string) []byte {
	result := content
	if len(result) > 0 && !strings.HasSuffix(string(result), "\n") {
		result = append(result, '\n')
	}
	result = append(result, []byte(line)...)
	result = append(result, '\n')
	return result
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
