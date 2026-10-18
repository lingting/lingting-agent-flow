package app

import (
	"context"
	"runtime"
	"time"

	"github.com/lingting/agentflow/internal/storage"
)

// 健康检查的总体状态取值。
const (
	// HealthStatusOK 表示全部检查通过。
	HealthStatusOK = "ok"
	// HealthStatusDegraded 表示 HTTP API 可用，但至少一项依赖不可用。
	HealthStatusDegraded = "degraded"
)

// 单项检查的名称与状态取值。
const (
	// CheckNameHTTPAPI 表示当前 HTTP 接口本身。
	CheckNameHTTPAPI = "http_api"
	// CheckNameDatabase 表示 SQLite 持久化。
	CheckNameDatabase = "database"

	// CheckStatusPass 表示该项检查通过。
	CheckStatusPass = "pass"
	// CheckStatusFail 表示该项检查失败。
	CheckStatusFail = "fail"
)

// defaultPingTimeout 是健康检查探测依赖的最长等待时间。
const defaultPingTimeout = 3 * time.Second

// HealthCheck 是单项依赖的检查结果。
type HealthCheck struct {
	// Name 是检查项名称，取值见 CheckName 系列常量。
	Name string `json:"name"`
	// Status 是该项结果，pass 或 fail。
	Status string `json:"status"`
	// Message 是失败原因，通过时为空。
	Message string `json:"message,omitempty"`
}

// Health 是健康检查响应。
//
// 它只描述 HTTP API 与本地持久化的可用性，不代表 SSH、Docker 或 Agent 已经可用。
type Health struct {
	// Status 是总体状态：全部通过为 ok，否则为 degraded。
	Status string `json:"status"`
	// Checks 是逐项检查结果。
	Checks []HealthCheck `json:"checks"`
	// CheckedAt 是本次检查时间。
	CheckedAt time.Time `json:"checked_at"`
}

// SystemInfo 是适合公开给本地 UI 的系统信息。
//
// 只包含构建期确定、不含凭据与路径的字段；数据库路径等本机信息不对外暴露。
type SystemInfo struct {
	// Version 是程序版本。
	Version string `json:"version"`
	// Commit 是构建时的提交号。
	Commit string `json:"commit"`
	// BuildDate 是构建时间。
	BuildDate string `json:"build_date"`
	// GoVersion 是编译程序使用的 Go 版本。
	GoVersion string `json:"go_version"`
	// Platform 是编译目标的 OS/架构。
	Platform string `json:"platform"`
}

// HealthService 汇总健康检查结果。
//
// 存储可以为空：数据库打开失败时服务仍然启动，健康检查如实报告 degraded，
// 而不是让整个 Web 页面无法访问。
type HealthService struct {
	store       storage.Store
	pingTimeout time.Duration
}

// NewHealthService 构造健康检查服务，store 允许为空。
func NewHealthService(store storage.Store) *HealthService {
	return &HealthService{store: store, pingTimeout: defaultPingTimeout}
}

// Check 执行健康检查。
//
// HTTP API 检查项在能进入本函数时必然通过；数据库检查通过一次真实查询确认，
// 不复用业务表读取，避免空库被误判为不可用。
func (s *HealthService) Check(ctx context.Context) Health {
	checks := []HealthCheck{{Name: CheckNameHTTPAPI, Status: CheckStatusPass}}
	checks = append(checks, s.checkDatabase(ctx))
	status := HealthStatusOK
	for _, check := range checks {
		if check.Status != CheckStatusPass {
			status = HealthStatusDegraded
			break
		}
	}
	return Health{Status: status, Checks: checks, CheckedAt: time.Now().UTC()}
}

// checkDatabase 检查 SQLite 是否可用。
func (s *HealthService) checkDatabase(ctx context.Context) HealthCheck {
	if s.store == nil {
		return HealthCheck{
			Name:    CheckNameDatabase,
			Status:  CheckStatusFail,
			Message: "数据库未初始化",
		}
	}
	pingCtx, cancel := context.WithTimeout(ctx, s.pingTimeout)
	defer cancel()
	if err := s.store.Ping(pingCtx); err != nil {
		return HealthCheck{
			Name:    CheckNameDatabase,
			Status:  CheckStatusFail,
			Message: "数据库不可用",
		}
	}
	return HealthCheck{Name: CheckNameDatabase, Status: CheckStatusPass}
}

// SystemInfo 返回当前构建与运行环境信息。
func (a *App) SystemInfo() SystemInfo {
	return SystemInfo{
		Version:   a.build.Version,
		Commit:    a.build.Commit,
		BuildDate: a.build.BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
