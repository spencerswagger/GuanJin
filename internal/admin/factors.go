package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**因子管理面**（接口清单 §3.8 factors 段，`AC-FLOW-05`）：
//
//	GET   /api/v1/factors        列出因子 + 能力声明（含平台级 enabled）
//	GET   /api/v1/factors/{type} 单个因子详情
//	PATCH /api/v1/factors/{type} 平台级启用/停用（写审计，与客户端级禁用是两回事）
//
// 三条口径：
//   - **平台级 ≠ 客户端级**：这里改的是"平台有没有这个因子"，客户端级禁用仍在
//     `login_policies.disabled_factors`；**有效禁用 = 两级并集**（编排层装配时取并集，
//     所以两级谁都不会覆盖谁）；
//   - **每次变更写审计**：落在 `audit_logs`（`action=factor.disable|enable`），
//     与配置更新同事务（见 `store.PlatformFactorRepo.SetEnabled`）；
//   - **三员**：读 = 系统管理员/审计员；写 = 系统管理员/安全管理员；审计员写 →
//     `THREE_POWER_VIOLATION`（不是 `INSUFFICIENT_ROLE`，两码要分开）。

// FactorSwitcher 是管理面需要的平台级因子状态能力面（实现是 `*store.PlatformFactorRepo`）。
type FactorSwitcher interface {
	Disabled(ctx context.Context) ([]string, error)
	SetEnabled(ctx context.Context, in store.SetFactorEnabledInput) error
}

// FactorsHTTP 是 factors 段三个端点的处理器。
type FactorsHTTP struct {
	reg      *factor.Registry
	switcher FactorSwitcher
	resolver session.TokenResolver
	logger   *slog.Logger
}

// NewFactorsHTTP 构造处理器（缺依赖即装配期失败）。
func NewFactorsHTTP(reg *factor.Registry, switcher FactorSwitcher, resolver session.TokenResolver,
	logger *slog.Logger) (*FactorsHTTP, error) {
	switch {
	case reg == nil, switcher == nil, resolver == nil, logger == nil:
		return nil, ErrNilDependency
	}
	return &FactorsHTTP{reg: reg, switcher: switcher, resolver: resolver, logger: logger}, nil
}

// Routes 挂上三个冻结路径。
func (h *FactorsHTTP) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/factors", h.list)
	mux.HandleFunc("GET /api/v1/factors/{type}", h.detail)
	mux.HandleFunc("PATCH /api/v1/factors/{type}", h.patch)
}

// AuthorizeFactors 判定 factors 面：读 = 系统管理员/审计员；写 = 系统管理员/安全管理员。
//
// 与 `AuthorizeSessions` 同一张矩阵写法：不在端点里写 `if role == ...`，
// 否则"某个端点忘了拦审计员"这类漏会在最不该漏的地方发生。
func AuthorizeFactors(c *session.Caller, write bool) error {
	return authorize(c,
		[]Role{RoleSystemAdmin, RoleAuditor},
		[]Role{RoleSystemAdmin, RoleSecurityAdmin},
		write)
}

// factorView 是列表/详情项（接口 §3.8：`{type, kind, enabled, capabilities[], descriptor}`）。
type factorView struct {
	Type         string              `json:"type"`
	Kind         string              `json:"kind"`
	Enabled      bool                `json:"enabled"`
	Capabilities []factor.Capability `json:"capabilities"`
	Descriptor   factor.Descriptor   `json:"descriptor"`
}

func (h *FactorsHTTP) list(w http.ResponseWriter, r *http.Request) {
	caller, ok := session.ResolveCaller(w, r, h.resolver)
	if !ok {
		return
	}
	if err := AuthorizeFactors(caller, false); err != nil {
		h.fail(w, r, err)
		return
	}
	views, err := h.views(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, h.logger, http.StatusOK, views)
}

func (h *FactorsHTTP) detail(w http.ResponseWriter, r *http.Request) {
	caller, ok := session.ResolveCaller(w, r, h.resolver)
	if !ok {
		return
	}
	if err := AuthorizeFactors(caller, false); err != nil {
		h.fail(w, r, err)
		return
	}
	typ := factor.FactorType(strings.TrimSpace(r.PathValue("type")))
	views, err := h.views(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for i := range views {
		if views[i].Type == string(typ) {
			writeJSON(w, h.logger, http.StatusOK, views[i])
			return
		}
	}
	// 未装配的因子：`FACTOR_NOT_SUPPORTED`（E4/422），与"已装配但被停用"分开。
	httpx.WriteError(w, traceID(r), httpx.CodeFactorNotSupported,
		"平台未装配该因子", map[string]any{"type": string(typ)})
}

// patchRequest 是 `PATCH /api/v1/factors/{type}` 的请求体（接口 §3.8：`{enabled, reason}`）。
//
// `Enabled` 用指针：`{"enabled":false}` 与"没给这个字段"必须区分——后者是缺参数，
// 不能当成"停用"（那会把一次畸形请求变成一次关停）。
type patchRequest struct {
	Enabled *bool  `json:"enabled"`
	Reason  string `json:"reason"`
}

// patchResponse 是 PATCH 的响应：只回变更后的平台级状态（客户端级不在这里，见文件头）。
type patchResponse struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	// DisabledFactors 是**平台级**当前被禁用的全部因子（便于页面直接刷新列表）。
	DisabledFactors []string `json:"disabled_factors"`
}

func (h *FactorsHTTP) patch(w http.ResponseWriter, r *http.Request) {
	caller, ok := session.ResolveCaller(w, r, h.resolver)
	if !ok {
		return
	}
	if err := AuthorizeFactors(caller, true); err != nil {
		h.fail(w, r, err)
		return
	}

	typ := string(factor.FactorType(strings.TrimSpace(r.PathValue("type"))))
	// 因子必须真的装配在平台里：否则"停用一个不存在的因子"会在 runtime_config 里
	// 留下垃圾，且页面会以为关掉了什么。
	if _, err := h.reg.Lookup(factor.FactorType(typ)); err != nil {
		httpx.WriteError(w, traceID(r), httpx.CodeFactorNotSupported,
			"平台未装配该因子", map[string]any{"type": typ})
		return
	}

	var body patchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httpx.WriteError(w, traceID(r), httpx.CodeInvalidRequest, "请求体不是合法 JSON", nil)
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, traceID(r), httpx.CodeMissingParameter, "缺少 enabled", nil)
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		// 停用是安全相关动作，必须说清为什么（审计里要能回答"谁在什么时候为什么关的"）。
		httpx.WriteError(w, traceID(r), httpx.CodeMissingParameter, "缺少 reason", nil)
		return
	}

	actor := caller.UserID
	if err := h.switcher.SetEnabled(r.Context(), store.SetFactorEnabledInput{
		Type:        typ,
		Enabled:     *body.Enabled,
		Reason:      strings.TrimSpace(body.Reason),
		ActorUserID: actor,
		ActorRole:   actingRole(caller),
		TraceID:     traceID(r),
		IP:          clientIP(r),
	}); err != nil {
		h.fail(w, r, err)
		return
	}

	disabled, err := h.switcher.Disabled(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, h.logger, http.StatusOK, patchResponse{
		Type:            typ,
		Enabled:         *body.Enabled,
		DisabledFactors: disabled,
	})
}

// views 把注册表 + 平台级禁用集合折成响应项。
func (h *FactorsHTTP) views(r *http.Request) ([]factorView, error) {
	descriptors, err := h.reg.Descriptors()
	if err != nil {
		return nil, err
	}
	disabled, err := h.switcher.Disabled(r.Context())
	if err != nil {
		return nil, err
	}
	off := make(map[string]bool, len(disabled))
	for _, t := range disabled {
		off[t] = true
	}
	out := make([]factorView, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, factorView{
			Type:         string(d.Type),
			Kind:         string(d.Kind),
			Enabled:      !off[string(d.Type)],
			Capabilities: d.Capabilities,
			Descriptor:   d,
		})
	}
	return out, nil
}

// actingRole 记录"以哪个平台角色执行"：按固定优先级取第一个调用方持有的写角色。
//
// 固定顺序（system_admin → security_admin）：同一请求可能持有多个角色，
// 审计里必须有一个确定值，否则同一次操作在不同副本上会记成不同角色。
func actingRole(c *session.Caller) string {
	if c == nil {
		return ""
	}
	for _, role := range []Role{RoleSystemAdmin, RoleSecurityAdmin} {
		if c.HasRole(string(role)) {
			return string(role)
		}
	}
	return ""
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return ""
}

// fail 按码表写出错误。
func (h *FactorsHTTP) fail(w http.ResponseWriter, r *http.Request, err error) {
	if code := CodeOf(err); code != "" {
		httpx.WriteError(w, traceID(r), code, messageOfAdmin(code), nil)
		return
	}
	// 存储层已判定的码（如 runtime_config 的安全约束）照用，不自造。
	if code := storeCodeOf(err); code != "" {
		httpx.WriteError(w, traceID(r), code, messageOfAdmin(code), nil)
		return
	}
	h.logger.Error("admin: unhandled error", "trace_id", traceID(r), "error", err)
	httpx.WriteError(w, traceID(r), httpx.CodeInternalError, "服务内部错误，请稍后重试", nil)
}

// storeCodeOf 取仓储层已判定的对外码（`*store.Error`），没有则空串。
func storeCodeOf(err error) httpx.Code {
	var se *store.Error
	if errors.As(err, &se) && se.Code != "" {
		return se.Code
	}
	return ""
}

func messageOfAdmin(c httpx.Code) string {
	switch c {
	case httpx.CodeInsufficientRole:
		return "当前账号没有该管理面权限"
	case httpx.CodeThreePowerViolation:
		return "该动作超出当前管理角色的职责域（审计员不得执行写操作）"
	case httpx.CodeFactorNotSupported:
		return "平台未装配该因子"
	case httpx.CodeInvalidRequest, httpx.CodeInvalidParam, httpx.CodeMissingParameter:
		return "请求参数不合法"
	default:
		return "请求无法完成"
	}
}
