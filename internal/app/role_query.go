package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 角色配置服务的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本；面向用户的说明
// 由错误本身携带，不包含内部实现细节。
var (
	// ErrRoleInvalidInput 表示提交的角色配置不合法，例如模型或系统提示词为空。
	ErrRoleInvalidInput = errors.New("app: 角色配置参数非法")
	// ErrRoleNotFound 表示角色不存在，或已被逻辑删除。
	ErrRoleNotFound = errors.New("app: 角色不存在")
)

// RoleService 提供固定角色配置的查询与编辑。
//
// 角色集合固定为四个，由服务启动时按内置默认定义补齐（app.RoleSeeds +
// storage.RoleSeeder.SeedRoles）；本服务不提供角色新增与删除，只允许修改
// system_prompt、tools_policy、model 与 enabled 四个字段，其中工具策略只能收敛在角色的
// 系统能力上限内；策略到 Pi 实际可用工具的映射由 domain.EffectivePiTools 统一计算。
type RoleService struct {
	store storage.Store
}

// NewRoleService 构造角色配置服务。
func NewRoleService(store storage.Store) (*RoleService, error) {
	if store == nil {
		return nil, fmt.Errorf("角色服务初始化失败: 存储不能为空")
	}
	return &RoleService{store: store}, nil
}

// List 返回全部未删除的角色，已禁用角色也在其中。
//
// 禁用角色不在这里过滤：接口保留它们用于诊断，创建任务时的选择器必须自行排除
// enabled=false 的记录，而任务创建本身仍由服务端校验角色可用性。
func (s *RoleService) List(ctx context.Context) ([]domain.Role, error) {
	roles, err := s.store.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询角色失败: %w", err)
	}
	return roles, nil
}

// Get 按角色标识读取单个角色的完整配置。
//
// 标识为空属于调用方参数错误；数据库中确实没有该角色时返回 ErrRoleNotFound，由接口层映射为
// 404，不伪造一份默认配置。
func (s *RoleService) Get(ctx context.Context, roleID string) (domain.Role, error) {
	id := domain.ID(strings.TrimSpace(roleID))
	if err := id.Validate(); err != nil {
		return domain.Role{}, fmt.Errorf("%w: 角色标识不能为空", ErrRoleInvalidInput)
	}
	role, err := s.store.GetRole(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Role{}, fmt.Errorf("%w: %s", ErrRoleNotFound, id)
		}
		return domain.Role{}, fmt.Errorf("读取角色失败: %w", err)
	}
	return *role, nil
}
