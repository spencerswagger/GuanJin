package admin

import (
	"errors"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是 S7 的**三员最小鉴权骨架**（m1-slices §4-S7；接口 §3.8 的「三员分离」）。
//
// 三条冻结语义（decisions.md D3-E③）：
//   - 调用超出本员职责域的写操作 → `THREE_POWER_VIOLATION`（E7/403）；
//   - 单纯缺该角色 → `INSUFFICIENT_ROLE`（E3/403）；
//   - **审计员不得执行任何写操作**（接口 §3.8 引言；AC-AUD-07 的"审计员写操作零成功"）。
//
// 为什么角色名是常量而不是从库里的字符串直接比：三员是**平台角色**，`role_bindings.role`
// 是业务系统自定义的角色名（开放集合）。三员名字一旦散落在各处字符串里，
// 改一处漏一处就会出现"某个端点忘了拦审计员"。常量集中在这里，端点只允许调 `Authorize*`。
//
// **待追认**：基线只给了三员的中文名（系统管理员/安全管理员/审计员），没有冻结它们的
// 字符串值；本切片取 `system_admin` / `security_admin` / `auditor` 三个小写下划线值，
// 作为**内部骨架标识**。若后续裁定要求别的拼写，只改这三个常量，端点不动。

// Role 是平台三员角色标识（**内部骨架值**，见文件头"待追认"）。
type Role string

// 平台三员。
const (
	// RoleSystemAdmin 系统管理员：基础设施（users/clients/keys/config/sessions）。
	RoleSystemAdmin Role = "system_admin"
	// RoleSecurityAdmin 安全管理员：授权与策略（policies/bindings/factors/sessions）。
	RoleSecurityAdmin Role = "security_admin"
	// RoleAuditor 审计员：只读（audit-logs + 只读查询），**写操作零成功**。
	RoleAuditor Role = "auditor"
)

// ErrInsufficientRole 表示调用方没有该端点要求的任何角色（`INSUFFICIENT_ROLE`，E3/403）。
var ErrInsufficientRole = errors.New("admin: insufficient role")

// ErrThreePowerViolation 表示调用方有角色、但该动作超出本员职责域
// （`THREE_POWER_VIOLATION`，E7/403）。
var ErrThreePowerViolation = errors.New("admin: three-power violation")

// CodeOf 把鉴权哨兵映射成冻结码。
func CodeOf(err error) httpx.Code {
	switch {
	case errors.Is(err, ErrInsufficientRole):
		return httpx.CodeInsufficientRole
	case errors.Is(err, ErrThreePowerViolation):
		return httpx.CodeThreePowerViolation
	default:
		return ""
	}
}

// allowAuditorWrite 是"审计员不得写"的唯一开关。
//
// 写成常量而不是参数：接口 §3.8 里这是硬约束（AC-AUD-07 遍历矩阵断言"审计员写操作零成功"），
// 一旦做成可配置项就等于把判据变成可选。
const allowAuditorWrite = false

// authorize 是矩阵判定的唯一实现：先判"有没有管理角色"，再判"这个动作在不在职责域内"。
func authorize(c *httpx.Caller, readRoles, writeRoles []Role, write bool) error {
	if c == nil {
		return ErrInsufficientRole
	}
	has := func(role Role) bool { return c.HasRole(string(role)) }

	if !write {
		for _, r := range readRoles {
			if has(r) {
				return nil
			}
		}
		// 读都不给：说明调用方根本不是三员之一（或角色名不在冻结集合里）。
		return ErrInsufficientRole
	}

	// 写操作：先看有没有写权限的角色；有写角色但其中含审计员且没有真正的写角色 → 越权。
	writeOK := false
	for _, r := range writeRoles {
		if has(r) {
			writeOK = true
			break
		}
	}
	if writeOK {
		return nil
	}
	if has(RoleAuditor) && !allowAuditorWrite {
		// 审计员调写端点：不是"缺角色"，而是"越出职责域"——两码必须分开（D3-E③），
		// 否则页面无法区分"你没这个角色"与"你不该做这件事"。
		return ErrThreePowerViolation
	}
	return ErrInsufficientRole
}

// AuthorizeSessions 判定 sessions 面（接口 §3.8 sessions 段）：
// 读 = 三员皆可；写 = 系统管理员 / 安全管理员。
func AuthorizeSessions(c *httpx.Caller, write bool) error {
	return authorize(c,
		[]Role{RoleSystemAdmin, RoleSecurityAdmin, RoleAuditor},
		[]Role{RoleSystemAdmin, RoleSecurityAdmin},
		write)
}
