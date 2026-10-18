package config_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/config"
)

// TestServerAddressUsesDefaults 校验未配置时使用回环地址与默认端口。
func TestServerAddressUsesDefaults(t *testing.T) {
	t.Parallel()
	var server config.ServerConfig

	if got := server.Address(); got != "127.0.0.1:26380" {
		t.Fatalf("默认监听地址应为 127.0.0.1:26380，实际 %q", got)
	}
}

// TestServerAddressHonoursConfig 校验显式配置的地址与端口生效。
func TestServerAddressHonoursConfig(t *testing.T) {
	t.Parallel()
	server := config.ServerConfig{Host: "127.0.0.1", Port: 26380}

	if got := server.Address(); got != "127.0.0.1:26380" {
		t.Fatalf("监听地址应为 127.0.0.1:26380，实际 %q", got)
	}
}

// TestServerAddressRejectsNonNumericPort 校验 IPv6 主机地址被正确加括号。
func TestServerAddressRejectsNonNumericPort(t *testing.T) {
	t.Parallel()
	server := config.ServerConfig{Host: "::1", Port: 26380}

	if got := server.Address(); got != "[::1]:26380" {
		t.Fatalf("IPv6 监听地址应为 [::1]:26380，实际 %q", got)
	}
}

// TestValidateRejectsInvalidServerPort 校验非法端口在配置校验阶段被拒绝。
func TestValidateRejectsInvalidServerPort(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, validYAML+`
server:
  host: 127.0.0.1
  port: 70000
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("非法端口应当校验失败")
	}
	if !strings.Contains(err.Error(), "server.port") {
		t.Fatalf("错误信息应指出 server.port，实际: %v", err)
	}
}

// TestLoadServerSection 校验 server 段可被正常加载。
func TestLoadServerSection(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, validYAML+`
server:
  host: 127.0.0.1
  port: 26381
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Server.Port != 26381 {
		t.Fatalf("端口应为 26381，实际 %d", cfg.Server.Port)
	}
	if got := cfg.Server.Address(); got != "127.0.0.1:26381" {
		t.Fatalf("监听地址应为 127.0.0.1:26381，实际 %q", got)
	}
}
