package cigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PermissionAPIVersion 是查询仓库权限时声明的 GitHub REST API 版本。
const PermissionAPIVersion = "2022-11-28"

// permissionResponseLimit 是权限接口响应的读取上限，避免异常响应被整体读入。
const permissionResponseLimit = 64 << 10

// writePermissions 是视为"具备仓库写入权限"的权限等级。
//
// GitHub 的 permission 字段只在 admin / maintain / write / triage / read / none 中取值；
// 前三个可以推送提交，后三个不可以。
var writePermissions = map[string]bool{
	"admin":    true,
	"maintain": true,
	"write":    true,
}

// GitHubPermissionChecker 通过 GitHub REST API 查询账号的仓库权限。
//
// 它只读取 /repos/{owner}/{repo}/collaborators/{login}/permission，不写入任何内容，
// 也不把令牌写入错误信息或日志。
type GitHubPermissionChecker struct {
	// APIURL 是 GitHub API 根地址，例如 https://api.github.com。
	APIURL string
	// Repository 是 owner/repo 形式的仓库全名。
	Repository string
	// Token 是只读访问令牌：CI 中由 github.token 提供。
	Token string
	// Client 是 HTTP 客户端，为空时使用带超时的默认客户端。
	Client *http.Client
}

// HasWritePermission 报告指定账号是否具备仓库写入权限。
//
// 任何无法确认的情况（地址或令牌缺失、请求失败、响应异常）都返回错误，由调用方按
// 拒绝处理：权限查不出来不等于有权限。
func (c GitHubPermissionChecker) HasWritePermission(
	ctx context.Context,
	login string,
) (bool, error) {
	trimmedLogin := strings.TrimSpace(login)
	if trimmedLogin == "" {
		return false, errors.New("账号为空")
	}
	apiURL := strings.TrimSuffix(strings.TrimSpace(c.APIURL), "/")
	if apiURL == "" {
		return false, errors.New("缺少 GitHub API 地址")
	}
	if strings.TrimSpace(c.Token) == "" {
		return false, errors.New("缺少 GitHub API 令牌")
	}
	owner, repository, err := splitRepository(c.Repository)
	if err != nil {
		return false, err
	}
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/collaborators/%s/permission",
		apiURL,
		url.PathEscape(owner),
		url.PathEscape(repository),
		url.PathEscape(trimmedLogin),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("构造权限查询请求失败: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	request.Header.Set("X-GitHub-Api-Version", PermissionAPIVersion)
	response, err := c.client().Do(request)
	if err != nil {
		return false, fmt.Errorf("请求权限接口失败: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("权限接口返回状态 %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, permissionResponseLimit))
	if err != nil {
		return false, fmt.Errorf("读取权限接口响应失败: %w", err)
	}
	var document struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return false, fmt.Errorf("解析权限接口响应失败: %w", err)
	}
	permission := strings.ToLower(strings.TrimSpace(document.Permission))
	if permission == "" {
		return false, errors.New("权限接口响应缺少 permission 字段")
	}
	return writePermissions[permission], nil
}

// client 返回实际使用的 HTTP 客户端。
func (c GitHubPermissionChecker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// splitRepository 校验并拆分 owner/repo 形式的仓库全名。
func splitRepository(repository string) (string, string, error) {
	trimmed := strings.TrimSpace(repository)
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("仓库全名 %q 不是 owner/repo 形式", trimmed)
	}
	return parts[0], parts[1], nil
}
