package guanjin

// CapabilityStatus 是一条能力在本构建里的落地状态。
//
// **不许静默缺**：`Implemented=false` 必须写清 `Reason` 与归属（哪个切片补），
// 否则 S11 的 AC 映射会漏项。
type CapabilityStatus struct {
	ID          string // GJ-CAP-NN（契约 §2.1，逐字符照抄）
	Name        string // 语言无关名（契约 §2.1）
	Required    bool   // 契约列「必须」= true
	Implemented bool
	Reason      string // 未实现时必填：为什么、什么时候补
}

// Capabilities 返回 22 条能力的落地状态（与 docs/sdk-contract.md §2.1 同序）。
//
// 本构建 = S9 **最小集**：验签 / 取键 / 缓存 / 强刷 / claim / jti / 剥离 / 读身份 /
// 注入上下文 / 公开路径 / 启动自检已实现；判定（12–18）与 Agent 面（20–21）未实现。
func Capabilities() []CapabilityStatus {
	return []CapabilityStatus{
		{ID: "GJ-CAP-01", Name: "VerifyAssertion", Required: true, Implemented: true},
		{ID: "GJ-CAP-02", Name: "SelectKeyByKid", Required: true, Implemented: true},
		{ID: "GJ-CAP-03", Name: "JwksCache", Required: true, Implemented: true},
		{ID: "GJ-CAP-04", Name: "RefreshOnUnknownKid", Required: true, Implemented: true},
		{ID: "GJ-CAP-05", Name: "ValidateExpIat", Required: true, Implemented: true},
		{ID: "GJ-CAP-06", Name: "ValidateIssAud", Required: true, Implemented: true},
		{ID: "GJ-CAP-07", Name: "ReplayGuard", Required: true, Implemented: true},
		{ID: "GJ-CAP-08", Name: "StripAssertionHeader", Required: true, Implemented: true},
		{ID: "GJ-CAP-09", Name: "ReadIdentity", Required: true, Implemented: true},
		{ID: "GJ-CAP-10", Name: "InjectHostContext", Required: true, Implemented: true},
		{ID: "GJ-CAP-11", Name: "AuthoritiesResolver", Required: false, Implemented: false,
			Reason: "可选扩展点：Go 侧以函数注入方式提供，S9 未接；补于 S10 前的接入打磨（不影响验签闭环）"},
		{ID: "GJ-CAP-12", Name: "AuthzEngine", Required: true, Implemented: false,
			Reason: "本地判定（三层 model.conf）属 M5 能力面，S9 只交付验签闭环；调用返回 ErrNotImplemented"},
		{ID: "GJ-CAP-13", Name: "AuthzModelBuilder", Required: true, Implemented: false,
			Reason: "同上（M5）；契约 §2.1 的 withClearance(level) 唯一写法在 M5 落地"},
		{ID: "GJ-CAP-14", Name: "RequirePermission", Required: true, Implemented: false,
			Reason: "同上（M5）"},
		{ID: "GJ-CAP-15", Name: "CheckRow", Required: true, Implemented: false,
			Reason: "同上（M5）；CheckOwner 同属此条"},
		{ID: "GJ-CAP-16", Name: "CheckLabel", Required: true, Implemented: false,
			Reason: "同上（M5）；形参名固定 objLevel，密级词表 1/2/3、公开不占级（契约 §2.1.2）"},
		{ID: "GJ-CAP-17", Name: "Scope", Required: true, Implemented: false,
			Reason: "同上（M5）"},
		{ID: "GJ-CAP-18", Name: "DefaultRole", Required: true, Implemented: false,
			Reason: "同上（M5）"},
		{ID: "GJ-CAP-19", Name: "PublicPathDeclare", Required: true, Implemented: true},
		{ID: "GJ-CAP-20", Name: "PolicyReport", Required: true, Implemented: false,
			Reason: "Agent 面上报（POST /api/v1/agent/report）依赖判定模型，随 M5 一起补；S9 不回源、不上报"},
		{ID: "GJ-CAP-21", Name: "PolicyPull", Required: false, Implemented: false,
			Reason: "可选能力；默认 agent.pull.enabled=false（方案 A 零回源，AC-SDK-10），故本构建不调用"},
		{ID: "GJ-CAP-22", Name: "StartupSelfCheck", Required: true, Implemented: true},
	}
}

// ErrCapabilityNotImplemented 为**未实现**能力返回带能力 ID 的错误（便于 AC 映射与
// 排障）；已实现的能力返回 nil（调用方据此不再二次判断）。
func ErrCapabilityNotImplemented(id string) error {
	for _, c := range Capabilities() {
		if c.ID != id {
			continue
		}
		if c.Implemented {
			return nil
		}
		return &Error{
			Code:    CodeInsufficientRole,
			Status:  403,
			Message: "能力 " + id + "（" + c.Name + "）本构建未实现：" + c.Reason,
			Err:     wrap(ErrNotImplemented, nil),
		}
	}
	return wrap(ErrNotImplemented, nil)
}
