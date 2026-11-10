package testkit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// PrivateKeyPath 在测试工作空间中生成一个未加密私钥文件并返回其路径。
//
// 本版本只支持无口令私钥认证，因此测试也只生成未加密私钥；文件落在
// TEST_ARTIFACT_DIR 之下并保留，便于失败时检查。
func PrivateKeyPath(t Tester) string {
	t.Helper()
	return PrivateKeyIn(t, Dir(t))
}

// PrivateKeyIn 在 dir 下生成一个未加密私钥文件并返回其路径。
//
// 新执行器的用例没有运行根目录，私钥落在编排进程下发的工作空间里，因此目录由调用方给出。
func PrivateKeyIn(t Tester, dir string) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "agentflow-test")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}
