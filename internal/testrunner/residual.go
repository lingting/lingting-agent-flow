package testrunner

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 零残留核验的状态取值（V3 R2 §6 / D1 冻结）。
const (
	// ResidualPassed 表示全量比对通过：本次运行没有留下新增资源。
	ResidualPassed = "passed"
	// ResidualFailed 表示存在残留，或存在无法确认的核验。
	ResidualFailed = "failed"
	// ResidualNotApplicable 表示本次运行没有分配任何资源，无需核验。
	ResidualNotApplicable = "not-applicable"
)

// residualEntry 是一条快照记录；key 用于在基线与终检之间稳定比对。
type residualEntry struct {
	key    string
	detail string
}

// residualSnapshot 是某一时刻全部提供者的资源快照。
type residualSnapshot struct {
	entries map[string]residualEntry
	errs    []string
}

// takeSnapshot 采集全部已登记提供者的资源快照。
//
// 快照只包含提供者能观测到的资源：进程内资源来自提供者自己的记录，远程资源由提供者
// 查询真实远端状态。查询失败会作为"无法确认"记录，不会静默当成空快照。
func takeSnapshot(ctx context.Context, providers *Providers) residualSnapshot {
	snapshot := residualSnapshot{entries: make(map[string]residualEntry)}
	if providers == nil {
		return snapshot
	}
	for _, resourceType := range providers.registeredTypes() {
		provider, err := providers.ProviderFor(resourceType)
		if err != nil {
			snapshot.errs = append(snapshot.errs, err.Error())
			continue
		}
		live, err := provider.Snapshot(ctx)
		if err != nil {
			snapshot.errs = append(snapshot.errs, fmt.Sprintf("%s 资源快照不完整: %v", resourceType, err))
		}
		for _, entry := range live {
			key := residualKey(entry)
			snapshot.entries[key] = residualEntry{key: key, detail: describeLiveResource(resourceType, entry)}
		}
	}
	sort.Strings(snapshot.errs)
	return snapshot
}

// residualKey 为快照记录生成跨快照稳定的键：优先使用资源的实际标识而不是声明名。
func residualKey(live LiveResource) string {
	handle := live.Handle
	resourceType := handle.Type
	if resourceType == "" {
		resourceType = "unknown"
	}
	for _, key := range []string{
		"address", "path", "container_id", "session_id", "host_id", "pid", "base_url",
	} {
		if value := handle.PayloadValue(key); value != "" {
			return resourceType + "|" + key + "=" + value
		}
	}
	return resourceType + "|name=" + handle.Name
}

// describeLiveResource 生成一条可读的残留说明。
func describeLiveResource(resourceType string, live LiveResource) string {
	detail := strings.TrimSpace(live.Detail)
	if detail == "" {
		detail = "仍存在的资源"
	}
	name := strings.TrimSpace(live.Handle.Name)
	if name == "" {
		return fmt.Sprintf("[%s] %s", resourceType, detail)
	}
	return fmt.Sprintf("[%s] %s（%s）", resourceType, detail, name)
}

// residualVerifier 负责运行级零残留终检：运行前记录基线，运行结束后全量比对。
type residualVerifier struct {
	baseline   residualSnapshot
	allocated  bool
	injections []string
}

// newResidualVerifier 采集基线快照并构造终检器。
func newResidualVerifier(ctx context.Context, providers *Providers) *residualVerifier {
	return &residualVerifier{baseline: takeSnapshot(ctx, providers)}
}

// markAllocated 记录本次运行分配过资源；终检因此不能退化成"无需核验"。
func (v *residualVerifier) markAllocated() {
	v.allocated = true
}

// addInjection 记录本次运行的故障注入来源，写入终检原因。
func (v *residualVerifier) addInjection(reason string) {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		v.injections = append(v.injections, trimmed)
	}
}

// finalize 采集终检快照并给出零残留结论。
//
// 比对口径：终检快照里出现、且基线里不存在的资源即为本次运行留下的残留；快照查询失败
// 记为"无法确认"，一律判 failed。基线里本来就存在的残留不计入本次运行，但在原因里写明。
func (v *residualVerifier) finalize(ctx context.Context, providers *Providers) ResidualCheck {
	final := takeSnapshot(ctx, providers)
	check := ResidualCheck{Status: ResidualPassed, CheckedAt: time.Now()}
	check.Baseline = snapshotDetails(v.baseline)
	residuals := make([]string, 0, len(final.entries))
	for key, entry := range final.entries {
		if _, existed := v.baseline.entries[key]; existed {
			continue
		}
		residuals = append(residuals, entry.detail)
	}
	sort.Strings(residuals)
	check.Residuals = residuals
	errors := make([]string, 0, len(v.baseline.errs)+len(final.errs))
	errors = append(errors, v.baseline.errs...)
	errors = append(errors, final.errs...)
	sort.Strings(errors)
	check.Errors = errors

	switch {
	case len(errors) > 0:
		check.Status = ResidualFailed
		check.Reason = "零残留核验无法确认"
	case len(residuals) > 0:
		check.Status = ResidualFailed
		check.Reason = fmt.Sprintf("运行结束后仍存在 %d 条残留资源", len(residuals))
	case !v.allocated && len(v.baseline.entries) == 0:
		check.Status = ResidualNotApplicable
		check.Reason = "本次运行没有分配任何资源，无需核验"
	default:
		check.Reason = "全量快照比对通过"
	}
	if len(v.baseline.entries) > 0 {
		check.Reason += fmt.Sprintf("；运行前基线已有 %d 条残留（不计入本次运行）", len(v.baseline.entries))
	}
	if len(v.injections) > 0 {
		check.Reason += "；" + strings.Join(v.injections, "；")
	}
	return check
}

// snapshotDetails 把快照压成可写入报告的说明列表。
func snapshotDetails(snapshot residualSnapshot) []string {
	details := make([]string, 0, len(snapshot.entries))
	for _, entry := range snapshot.entries {
		details = append(details, entry.detail)
	}
	sort.Strings(details)
	return details
}

// verifyResourceRelease 调用提供者自证资源已消失，并把结论写回资源结果。
//
// 自证失败或无法确认时保留 released=false 之外的独立字段：释放成功但自证失败同样是失败，
// 不能被"释放调用没报错"掩盖。
func verifyResourceRelease(
	ctx context.Context,
	providers *Providers,
	result *ResourceResult,
	handle Handle,
) {
	provider, err := providers.ProviderFor(result.Type)
	if err != nil {
		result.VerifyError = err.Error()
		return
	}
	if verifyErr := provider.Verify(ctx, handle); verifyErr != nil {
		result.VerifyError = verifyErr.Error()
		return
	}
	result.Verified = true
}
