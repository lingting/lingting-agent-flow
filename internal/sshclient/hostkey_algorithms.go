package sshclient

import (
	"bufio"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// HostKeyTypeProvider 由能给出该主机已记录主机密钥类型的主机密钥库实现。
//
// Dialer 用它实现"按 known_hosts 记录依次协商"：服务端同时提供多种主机密钥时，
// 逐个用已记录的类型握手，取第一个校验通过的结果。
type HostKeyTypeProvider interface {
	// RecordedHostKeyTypes 返回该主机在信任记录中出现过的密钥类型，按记录顺序去重。
	RecordedHostKeyTypes(host string, port int) []string
}

// RecordedHostKeyTypes 返回 known_hosts 中该主机条目已记录的密钥类型。
//
// 返回顺序与文件中的记录顺序一致并已去重；没有任何记录时返回空切片，调用方据此
// 使用库默认的算法协商顺序。哈希化的主机名无法反查，这类记录会被跳过。
func (s *KnownHostsStore) RecordedHostKeyTypes(host string, port int) []string {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	seen := make(map[string]struct{})
	var types []string
	for _, path := range s.existingPaths() {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		types = append(types, readRecordedKeyTypes(file, address, seen)...)
		_ = file.Close()
	}
	return types
}

// recordedHostKeyTypes 返回主机密钥库给出的已记录类型，不支持查询时返回空。
func recordedHostKeyTypes(store HostKeyStore, host string, port int) []string {
	provider, ok := store.(HostKeyTypeProvider)
	if !ok {
		return nil
	}
	return provider.RecordedHostKeyTypes(host, port)
}

// readRecordedKeyTypes 从单个 known_hosts 文件内容中提取匹配主机的密钥类型。
func readRecordedKeyTypes(reader io.Reader, address string, seen map[string]struct{}) []string {
	var types []string
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// 合法记录至少是 "主机 算法 base64"；注释与 @cert-authority、@revoked 行跳过。
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "@") {
			continue
		}
		if !hostFieldMatches(fields[0], address) {
			continue
		}
		if _, done := seen[fields[1]]; done {
			continue
		}
		seen[fields[1]] = struct{}{}
		types = append(types, fields[1])
	}
	return types
}

// hostFieldMatches 判断 known_hosts 的主机字段是否匹配目标地址。
//
// 支持逗号分隔的多主机、通配符 * 与 ?，以及 host:port 形式；哈希化主机名
// （以 |1| 开头）无法反查，直接视为不匹配。
func hostFieldMatches(field, address string) bool {
	normalized := knownhostsNormalize(address)
	for _, pattern := range strings.Split(field, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "|") {
			continue
		}
		if pattern == normalized || pattern == address {
			return true
		}
		if wildcardMatch(pattern, normalized) || wildcardMatch(pattern, address) {
			return true
		}
	}
	return false
}

// knownhostsNormalize 把 host:port 转换为 known_hosts 的书写形式。
//
// 与 knownhosts.Normalize 一致：端口为 22 时省略端口，IPv6 地址加方括号。
func knownhostsNormalize(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if port == "22" {
		return host
	}
	// known_hosts 的非默认端口写法固定为 "[host]:port"，IPv6 地址同样适用。
	return "[" + host + "]:" + port
}

// wildcardMatch 实现 OpenSSH 风格的 * 与 ? 通配匹配。
func wildcardMatch(pattern, value string) bool {
	for {
		switch {
		case len(pattern) == 0:
			return len(value) == 0
		case len(value) == 0:
			return false
		case pattern[0] == '*':
			if len(pattern) == 1 {
				return true
			}
			for index := range value {
				if wildcardMatch(pattern[1:], value[index:]) {
					return true
				}
			}
			return false
		case pattern[0] == '?' || pattern[0] == value[0]:
			pattern = pattern[1:]
			value = value[1:]
		default:
			return false
		}
	}
}

// 保证默认实现满足已记录类型查询接口。
var _ HostKeyTypeProvider = (*KnownHostsStore)(nil)
