package planning_remote

import (
	"crypto/rand"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
)

// 读取验收样本的选取约束。
const (
	// sourceReadMinConstantLength 是可用于读取验收的字符串常量最短长度。
	//
	// 取值必须无法从常量命名推断，也不能短到模型可以蒙对。
	sourceReadMinConstantLength = 16
	// sourceReadMaxScannedFiles 是读取验收最多解析的候选源码文件数。
	//
	// 每次读取都是一次真实远程 git 调用，限制数量让候选集合足够丰富，又不把
	// 一次验收变成全仓库遍历。
	sourceReadMaxScannedFiles = 12
)

// TestRemotePlanningReadsPinnedProjectContent 验收真实规划角色读取固定 commit 的源码内容。
//
// 覆盖：
//   - 生产 Prepare 建立的固定 commit 只读快照；
//   - 测试侧通过独立控制连接、以只读 git 命令取得的真实源码与 Go AST 常量样本；
//   - 生产 PlanningAgentService.RunRole 发起的真实需求分析师 Pi 调用；
//   - 提示词只含源码路径与常量标识符，模型必须真实读取文件才能答出常量值；
//   - 规划角色的有效工具策略仍然不含 edit / write / bash；
//   - 调用之后快照仍固定在原 commit、工作区干净，容器 / 快照卷 / Session 路径 /
//     归档仍遵守既有稳定绑定规则。
//
// 该用例不使用 Fake Pi、Mock Runner、假文件内容，也不直接调用容器内 Pi CLI 代替生产
// 规划 Agent 调用。
func TestRemotePlanningReadsPinnedProjectContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.newSession(t, "源码读取验收：只读取当前仓库，不修改任何文件。")
	detail := f.prepare(t, session)
	defer f.release(t, session)
	f.injectPiConfig(t, detail.Container.ID)

	// 样本来自快照中真实 Git 跟踪文件；测试侧的读取路径与生产规划服务无关。
	sample := f.selectSourceConstant(t, detail)
	t.Logf(
		"SOURCE_SAMPLE file=%s const=%s value=%s commit=%s",
		sample.path,
		sample.ident,
		sample.value,
		detail.SourceCommit,
	)

	prompt := fmt.Sprintf(
		"请用可用的只读工具读取项目源码文件 %s，找到其中名为 %s 的字符串常量声明，"+
			"把该常量的值原样返回。只回复这个值本身，不要解释，不要修改任何文件。",
		sample.path,
		sample.ident,
	)
	// 提示词泄露期望值会让"通过"不再证明模型读过文件。
	if strings.Contains(prompt, sample.value) {
		t.Fatalf("提示词包含期望的常量值，读取验收不成立: 常量 %s", sample.ident)
	}

	result := f.runRoleOK(t, session, detail, domain.RoleTypeRequirementAnalyst, prompt)
	if !strings.Contains(result.Result.FinalText, sample.value) {
		t.Fatalf(
			"真实 Pi 输出没有包含独立读取得到的常量值 %q: 文件 %s，常量 %s，实际输出 %q",
			sample.value,
			sample.path,
			sample.ident,
			truncate(result.Result.FinalText, 600),
		)
	}
	// 记录模型实际回答，便于事后核对"回答的是常量值"而不是复述提示词。
	t.Logf("SOURCE_READ_ANSWER %q", truncate(result.Result.FinalText, 300))
	t.Logf(
		"SOURCE_READ_OK file=%s const=%s container=%s session_path=%s archive=%s",
		sample.path,
		sample.ident,
		result.Container.DockerID,
		result.ContainerSessionPath,
		result.ArchivePath,
	)

	// 工具策略与文件系统只读是两层独立约束：读取验收成立之后仍然不允许写入工具。
	assertRoleToolCeiling(t, f)
	// 真实读取之后快照必须仍在原 commit 上，且没有工作区改动。
	assertSnapshotClean(t, f, detail.Container.ID, detail.SourceCommit)

	f.assertStableSourceReadBinding(t, session, detail, result)
}

// sourceConstant 是快照中可用于读取验收的字符串常量样本。
type sourceConstant struct {
	// path 是常量所在文件在快照中的仓库相对路径。
	path string
	// ident 是常量标识符。
	ident string
	// value 是通过独立只读路径从真实源码取得的常量值。
	value string
}

// selectSourceConstant 从快照里随机挑选一个满足条件的字符串常量样本。
//
// 样本来源是快照中真实的 Git 跟踪文件：文件清单与文件内容都由独立控制连接上的只读
// git 命令取得，测试进程不写入快照，也不把源码全文发给模型。
func (f *fixture) selectSourceConstant(
	t *testing.T,
	detail app.PlanningRuntimeDetail,
) sourceConstant {
	t.Helper()
	files := shuffleRandom(t, f.snapshotSourceFiles(t, detail.Container.DockerID))
	if len(files) == 0 {
		t.Fatalf(
			"快照 %s 中没有 internal/ 或 cmd/ 下的 Go 源文件，无法建立读取验收样本",
			detail.SourceCommit,
		)
	}
	pool := make([]sourceConstant, 0, sourceReadMaxScannedFiles)
	seen := make(map[string]struct{}, sourceReadMaxScannedFiles)
	for index, file := range files {
		if index >= sourceReadMaxScannedFiles {
			break
		}
		source := f.readSnapshotFile(t, detail.Container.DockerID, file)
		for _, candidate := range collectSourceConstants(t, file, source) {
			if _, duplicated := seen[candidate.value]; duplicated {
				continue
			}
			seen[candidate.value] = struct{}{}
			pool = append(pool, candidate)
		}
	}
	if len(pool) == 0 {
		t.Fatalf(
			"快照 %s 中没有长度不低于 %d 且带数字后缀的字符串常量，无法建立读取验收样本",
			detail.SourceCommit,
			sourceReadMinConstantLength,
		)
	}
	t.Logf("SOURCE_CANDIDATES count=%d scanned_files=%d", len(pool), minInt(len(files), sourceReadMaxScannedFiles))
	return pool[randomIndex(t, len(pool))]
}

// snapshotSourceFiles 列出快照中 internal/ 与 cmd/ 下的 Go 源文件。
func (f *fixture) snapshotSourceFiles(t *testing.T, dockerID string) []string {
	t.Helper()
	output := f.snapshotGit(t, dockerID, "ls-tree", "-r", "--name-only", "HEAD")
	files := make([]string, 0, 16)
	for _, line := range strings.Split(output, "\n") {
		file := strings.TrimSpace(line)
		if !strings.HasPrefix(file, "internal/") && !strings.HasPrefix(file, "cmd/") {
			continue
		}
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		files = append(files, file)
	}
	return files
}

// readSnapshotFile 通过独立控制连接以只读方式读取快照中的文件内容。
//
// 读取的是 Git 对象库里的 blob，而不是工作区文件：它既不受工作区改动影响，也不会
// 触发任何写操作。
func (f *fixture) readSnapshotFile(t *testing.T, dockerID, file string) string {
	t.Helper()
	return f.snapshotGit(t, dockerID, "cat-file", "blob", "HEAD:"+file)
}

// snapshotGit 在规划容器内执行一条只读 git 命令。
//
// 它只用于读取：容器 ID 必须由调用方给出，命令由固定子命令与转义后的参数构造，
// 不经过生产容器服务，也不触发任何写入。
func (f *fixture) snapshotGit(t *testing.T, dockerID string, args ...string) string {
	t.Helper()
	if dockerID == "" {
		t.Fatalf("缺少规划容器标识，无法通过独立连接读取快照")
	}
	parts := []string{
		"docker", "exec", shellQuote(dockerID),
		"git", "-C", shellQuote(planningWorkDir),
	}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	run, err := f.runner.Run(f.ctx, "docker exec", strings.Join(parts, " "), planningCommitTimeout)
	if err != nil || !run.Succeeded() {
		t.Fatalf(
			"通过独立连接读取快照失败: git %v: %v（退出码 %d）: %s",
			args,
			err,
			run.ExitCode,
			truncate(strings.TrimSpace(run.Stderr), 400),
		)
	}
	return run.Stdout
}

// collectSourceConstants 用 Go AST 从真实源码中收集符合条件的字符串常量。
func collectSourceConstants(t *testing.T, file, source string) []sourceConstant {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatalf("解析快照源码 %s 失败: %v", file, err)
	}
	found := make([]sourceConstant, 0, 8)
	for _, decl := range parsed.Decls {
		generic, ok := decl.(*ast.GenDecl)
		if !ok || generic.Tok != token.CONST {
			continue
		}
		for _, spec := range generic.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok || len(values.Names) != len(values.Values) {
				continue
			}
			for index, name := range values.Names {
				literal, ok := values.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil || !distinctiveConstantValue(unquoted) {
					continue
				}
				found = append(found, sourceConstant{
					path:  file,
					ident: name.Name,
					value: unquoted,
				})
			}
		}
	}
	return found
}

// distinctiveConstantValue 判断字符串常量是否适合作为读取验收样本。
//
// 取值必须足够长、只含便于逐字比对的字符，并且带数字后缀：模型无法从常量命名推断
// 内容，也无法用一句泛泛的"读过文件"蒙对。
func distinctiveConstantValue(value string) bool {
	if len(value) < sourceReadMinConstantLength {
		return false
	}
	hasDigit := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
			hasDigit = true
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return hasDigit
}

// shuffleRandom 用密码学随机源打乱切片副本，避免固定挑选同一个候选。
func shuffleRandom(t *testing.T, values []string) []string {
	t.Helper()
	shuffled := make([]string, len(values))
	copy(shuffled, values)
	for index := len(shuffled) - 1; index > 0; index-- {
		swap := randomIndex(t, index+1)
		shuffled[index], shuffled[swap] = shuffled[swap], shuffled[index]
	}
	return shuffled
}

// randomIndex 返回 [0, limit) 内的随机下标。
func randomIndex(t *testing.T, limit int) int {
	t.Helper()
	if limit <= 0 {
		t.Fatalf("随机下标上限必须为正数，实际 %d", limit)
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		t.Fatalf("生成随机下标失败: %v", err)
	}
	return int(value.Int64())
}

// minInt 返回两个整数中较小的一个。
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// assertStableSourceReadBinding 核验一次读取验收没有改变既有稳定绑定。
//
// 容器、快照卷、容器内 Session 路径与本机归档全部由服务端按固定规则生成，读取验收
// 之后必须与准备阶段一致。
func (f *fixture) assertStableSourceReadBinding(
	t *testing.T,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
	result app.PlanningAgentResult,
) {
	t.Helper()
	if result.Container.DockerID != detail.Container.DockerID {
		t.Fatalf("读取验收使用了另一个规划容器: 期望 %s，实际 %s",
			detail.Container.DockerID, result.Container.DockerID)
	}
	f.assertBindingIntact(t, session, detail)
	current := f.reloadSession(t, session.ID)
	if current.AnalystSessionID != result.RoleSessionID {
		t.Fatalf("需求分析师角色会话标识没有被稳定绑定: 记录 %s，调用 %s",
			current.AnalystSessionID, result.RoleSessionID)
	}
	expectedSessionPath := path.Join(
		execenv.DefaultSessionDir,
		session.ID.String(),
		result.RoleSessionID,
		"session.jsonl",
	)
	if result.ContainerSessionPath != expectedSessionPath {
		t.Fatalf("容器内 Session 路径不符合固定规则: 期望 %s，实际 %s",
			expectedSessionPath, result.ContainerSessionPath)
	}
	// 本机归档路径由服务端按 filepath 规则生成，必须落在 Session 根目录的固定位置。
	expectedArchive := filepath.Join(
		f.sessionRoot,
		"planning",
		session.ID.String(),
		result.RoleSessionID,
		"session.jsonl",
	)
	if result.ArchivePath != expectedArchive {
		t.Fatalf("本机归档路径不符合固定规则: 期望 %s，实际 %s",
			expectedArchive, result.ArchivePath)
	}
	assertArchiveComplete(t, result.ArchivePath, "")
	raw := f.inspectRaw(t, detail.Container.DockerID)
	mount, found := raw.mountFor(planningWorkDir)
	if !found {
		t.Fatalf("读取验收后快照卷不再挂载到 %s: %+v", planningWorkDir, raw.Mounts)
	}
	if mount.Name != detail.CodeVolumeName || mount.RW {
		t.Fatalf("读取验收后快照卷绑定被改变: 期望只读挂载 %s，实际 %+v",
			detail.CodeVolumeName, mount)
	}
	t.Logf(
		"SOURCE_READ_BINDING container=%s docker_id=%s volume=%s session_path=%s archive=%s",
		detail.Container.ID,
		detail.Container.DockerID,
		detail.CodeVolumeName,
		result.ContainerSessionPath,
		result.ArchivePath,
	)
}
