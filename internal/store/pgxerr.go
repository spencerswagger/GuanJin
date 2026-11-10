package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 业务错误码。**一律取自接口 §5.4**（D3-B：只有一套码，`IAM_*` 与自造别名码均作废）。
//
// 这里用 httpx.Code 而不是自己再定义一套字符串：码表只能有一个来源，
// 否则 HTTP 层与数据层迟早各说一套（跨包一致性由 pgxerr_test.go 守住）。
const (
	codeIdentifierAlreadyBound = httpx.CodeIdentifierAlreadyBound // 409
	codeBindingAlreadyExists   = httpx.CodeBindingAlreadyExists   // 409
	codePolicySnapshotConflict = httpx.CodePolicySnapshotConflict // 409
	codeCooldownActive         = httpx.CodeCooldownActive         // 409
	codeTierDirectionViolation = httpx.CodeTierDirectionViolation // 422
	codeUserNotFound           = httpx.CodeUserNotFound           // 404
	codeClientNotFound         = httpx.CodeClientNotFound         // 404
	codeIdentifierNotFound     = httpx.CodeIdentifierNotFound     // 404
	codeSessionNotFound        = httpx.CodeSessionNotFound        // 404
	codeFlowNotFound           = httpx.CodeFlowNotFound           // 404
	codeInvalidParameter       = httpx.CodeInvalidParam           // 400
	codeMissingParameter       = httpx.CodeMissingParameter       // 400
	codeInvalidClient          = httpx.CodeInvalidClient          // 400
	codeInvalidRequest         = httpx.CodeInvalidRequest         // 400
	codeDBUnavailable          = httpx.CodeDBUnavailable          // 503
)

// Error 是"数据库错误 → 业务错误"的唯一载体。
//
// 携带四个事实，缺一个都会让排障变成猜：
//   - SQLState：PostgreSQL 的 5 位错误类（23505/23514/…）；
//   - Constraint：约束名（**双键映射的第二把钥匙**，D4-D）；
//   - Code/Status：对外错误码与 HTTP 状态（取自 httpx，唯一码表）；
//   - 原始错误：`Unwrap` 保留，日志里能看到 PG 的 detail。
//
// **不含用户可见文案**：message 由 HTTP 层按 code 统一给（具体文案是页面规格的事）。
type Error struct {
	SQLState   string
	Constraint string
	Table      string
	Column     string
	Code       httpx.Code
	Status     int
	// Reason 是"为什么这么映"的一句话（来自映射表），写进排障日志用。
	Reason string
	cause  error
}

// Error 实现 error 接口。文本含 SQLSTATE 与约束名——**不含 SQL 与参数值**（防泄露敏感数据）。
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == "" {
		return fmt.Sprintf("store: unmapped db error (sqlstate=%s constraint=%s table=%s)",
			e.SQLState, e.Constraint, e.Table)
	}
	return fmt.Sprintf("store: %s (sqlstate=%s constraint=%s table=%s)",
		e.Code, e.SQLState, e.Constraint, e.Table)
}

// Unwrap 保留原始 pgconn 错误，供日志/诊断使用。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is 让 `errors.Is(err, ErrCodes.X)` 这类按码比较可用（比较 code 而不是指针）。
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code && e.SQLState == t.SQLState
}

// Mapped 报告该错误是否映射到了对外的 §5.4 码。
// false 表示"属缺陷或未登记约束"——HTTP 层应回 INTERNAL_ERROR，并当作 bug 排查。
func (e *Error) Mapped() bool { return e != nil && e.Code != "" }

// SQLSTATE 常量（PostgreSQL 错误码文档 Appendix A）。
const (
	sqlStateUniqueViolation            = "23505"
	sqlStateForeignKeyViolation        = "23503"
	sqlStateCheckViolation             = "23514"
	sqlStateNotNullViolation           = "23502"
	sqlStateRestrictViolation          = "23001" // forbid_mutation() 触发器（只增表保护，I5）
	sqlStateInvalidTextRepr            = "22P02"
	sqlStateNumericOutOfRange          = "22003"
	sqlStateSerializationFail          = "40001"
	sqlStateDeadlockDetected           = "40P01"
	sqlStateUndefinedTable             = "42P01"
	sqlStateUndefinedColumn            = "42703"
	sqlStateConnectionFailure          = "08006"
	sqlStateSQLClientUnableToEstablish = "08001" // SQLCLIENT_UNABLE_TO_ESTABLISH_SQLCONNECTION
	sqlStateProtocolViolation          = "08P01" // PROTOCOL_VIOLATION（连接中途断开）
	sqlStateTooManyConnections         = "53300"
	sqlStateAdminShutdown              = "57P01" // 运维/容器停库（AC-FC-02 的真实停库常走这条）
	sqlStateCrashShutdown              = "57P02" // 数据库崩溃
	sqlStateCannotConnectNow           = "57P03" // 启动中/正在恢复
)

// constraintRule 是「(SQLSTATE, 约束名) → 业务码」的一条规则。
type constraintRule struct {
	code   httpx.Code
	reason string // 为什么这么映（评审时要能一眼看懂，不需要去翻文档）
}

// uniqueRules 是 23505 的映射表：**按约束名逐条登记**。
//
// 禁止"23505 一律 409"（D4-D / AC-ENG-04）：同一个 SQLSTATE 下，
// 「标识符已被绑定」和「码值重复」对用户是完全不同的两件事。
// 新增唯一约束时**必须**在这张表里补一行，否则单测 TestEveryUniqueConstraintIsMapped 会红。
//
// 行号口径 = `decisions.md` **D4-D₂（CH-7）八行映射表**，改这里必须同步那份文档：
//
//	CH-7① identifiers_type_value_uniq        → IDENTIFIER_ALREADY_BOUND (E4/409)
//	CH-7② users_username_lower_uidx           → IDENTIFIER_ALREADY_BOUND (E4/409)
//	CH-7③ role_bindings_uniq                  → BINDING_ALREADY_EXISTS   (E7/409)
//	CH-7④ policy_snapshots_uniq               → POLICY_SNAPSHOT_CONFLICT (E4/409)
//	CH-7⑤ flows_one_active_per_user_client_uniq → COOLDOWN_ACTIVE        (E4/409)
//	CH-7⑥ clients_pkey                        → INVALID_CLIENT           (E1/400)
//	CH-7⑦ signing_keys_kid_uniq               → INVALID_PARAMETER        (E1/400)
//	CH-7⑧ login_policies_tier_default_uidx / login_policies_client_override_uidx
//	                                          → INVALID_PARAMETER        (E1/400)
//
// 另外两条**故意不映射**（见 unmappedUniqueConstraints）：identifiers_one_primary_per_user
// （D3-B：I6 无对外错误码）、user_visited_clients_uniq（upsert 前提，撞上即缺陷）。
var uniqueRules = map[string]constraintRule{
	// CH-7①
	"identifiers_type_value_uniq": {
		code:   codeIdentifierAlreadyBound,
		reason: "标识符 (type,value) 全局唯一 → 该标识符已被绑定（E4/409）",
	},
	// CH-7②
	"users_username_lower_uidx": {
		code:   codeIdentifierAlreadyBound,
		reason: "用户名大小写不敏感唯一 → 视作标识符已被绑定（E4/409）",
	},
	// CH-7③
	"role_bindings_uniq": {
		code:   codeBindingAlreadyExists,
		reason: "角色绑定 (user_id,client_id,role) 唯一 → 绑定已存在（E7/409）",
	},
	// CH-7④
	"policy_snapshots_uniq": {
		code:   codePolicySnapshotConflict,
		reason: "策略快照 (client_id,version) 唯一 → 版本冲突（E4/409）",
	},
	// CH-7⑤
	"flows_one_active_per_user_client_uniq": {
		code: codeCooldownActive,
		reason: "同一用户对同一 client 至多一条活跃流（D5）→ 409；" +
			"§5.4 无「已有活跃流」专用码，CH-7 定取 COOLDOWN_ACTIVE（E4/409）。" +
			"**正常路径必须先查后建**（已有活跃流 → 直接复用同一条 flow_id 继续走），" +
			"本映射仅作并发竞态兜底；否则客户端会把「接着走」误读成「要等」",
	},
	// CH-7⑥
	"clients_pkey": {
		code:   codeInvalidClient,
		reason: "client_id 主键冲突 → 该 client_id 已存在（§5.4 无专用码，取 INVALID_CLIENT/400）",
	},
	// CH-7⑦
	"signing_keys_kid_uniq": {
		code:   codeInvalidParameter,
		reason: "kid 唯一冲突（§5.4 无专用码，取 INVALID_PARAMETER/400）",
	},
	// CH-7⑧
	"login_policies_tier_default_uidx": {
		code:   codeInvalidParameter,
		reason: "每个档位仅一份默认策略（§5.4 无专用码，取 INVALID_PARAMETER/400）",
	},
	"login_policies_client_override_uidx": {
		code:   codeInvalidParameter,
		reason: "每个 client 仅一份覆盖策略（§5.4 无专用码，取 INVALID_PARAMETER/400）",
	},
}

// checkRules 是 23514 的映射表：默认 400 INVALID_PARAMETER，**例外逐条登记**。
//
// 这就是"双键"在 CHECK 侧的意义：`login_policies_refresh_le_absolute` 违反的是
// **方向不变式**（I8），必须 422 `TIER_DIRECTION_VIOLATION`，不能和普通参数错误混成 400。
var checkRules = map[string]constraintRule{
	"login_policies_refresh_le_absolute": {
		code:   codeTierDirectionViolation,
		reason: "refresh_token_ttl ≤ 会话绝对超时（I8 方向不变式）→ 422",
	},
	"runtime_config_no_master_key": {
		code:   codeInvalidParameter,
		reason: "主密钥类键名禁止写入 runtime_config → 400（安全约束，绝不能当成功）",
	},
	"challenges_purpose_valid": {
		code:   codeInvalidParameter,
		reason: "purpose 六值词表（D2）→ 400",
	},
	"sessions_acr_check": {
		code:   codeInvalidParameter,
		reason: "acr 取值域（D12）→ 400，不得当 500（D12 明确要求）",
	},
	"flows_completed_requires_user": {
		code:   codeInvalidParameter,
		reason: "I-F1：status='completed' 必须 user_id NOT NULL → 400（D5/AC-FLOW-11①）",
	},
	"flows_consumed_consistency": {
		code:   codeInvalidParameter,
		reason: "I-F4：completed 与 consumed_at 同真同假 → 400",
	},
}

// fkRules 是 23503 的映射表：外键违反 = **被引用的对象不存在** → 用 §5.4 的 *_NOT_FOUND（404）。
//
// PG 的外键名是「<表>_<列>_fkey」，由迁移里的 REFERENCES 自动生成；
// 这里按**生成后的名字**登记，改名即测试失败（避免"改了迁移没改映射"的静默降级）。
var fkRules = map[string]constraintRule{
	"flows_user_id_fkey":          {code: codeUserNotFound, reason: "flows.user_id → users(id)"},
	"flows_session_id_fkey":       {code: codeSessionNotFound, reason: "flows.session_id → sessions(id)"},
	"flows_client_id_fkey":        {code: codeClientNotFound, reason: "flows.client_id → clients(client_id)"},
	"sessions_user_id_fkey":       {code: codeUserNotFound, reason: "sessions.user_id → users(id)"},
	"sessions_client_id_fkey":     {code: codeClientNotFound, reason: "sessions.client_id → clients(client_id)"},
	"identifiers_user_id_fkey":    {code: codeUserNotFound, reason: "identifiers.user_id → users(id)"},
	"authenticators_user_id_fkey": {code: codeUserNotFound, reason: "authenticators.user_id → users(id)"},
	"challenges_user_id_fkey":     {code: codeUserNotFound, reason: "challenges.user_id → users(id)"},
	"challenges_client_id_fkey":   {code: codeClientNotFound, reason: "challenges.client_id → clients(client_id)"},
	"challenges_identifier_id_fkey": {
		code:   codeIdentifierNotFound,
		reason: "challenges.identifier_id → identifiers(id)",
	},
}

// MapError 把数据库错误翻译成业务错误（AC-ENG-04 / 技术栈 §5.3）。
//
// 规则：
//   - nil → nil；非 PG 错误原样返回（上层自己判断，不吞）；
//   - 23505 / 23514 / 23503：**先看 SQLSTATE、再按约束名查表**（双键）；
//   - 未登记的 23505/23514：返回 `Mapped()==false` 的 Error（HTTP 层回 500，且这是缺陷）；
//   - 40001/40P01：标记为可重试（Retryable），不吞成业务码；
//   - 42P01/42703（表/列不存在）：schema 漂移 = 缺陷，不映射成业务码。
//
// **不得把原始 DB 错误抛到 HTTP 层**（技术栈 §5.3）——所以仓储的每个写操作都要过这里。
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		// 连接层失败（PG 进程被停、端口不通、连接被重置）**不是** PgError：
		// 原来会原样上抛，HTTP 层只能回 500，而 AC-FC-02 要 **503 `DB_UNAVAILABLE`**。
		// 判定边界见 `unavailable.go`（只认连接/会话层，不认业务错误）。
		if IsUnavailable(err) {
			return &Error{
				Code:   codeDBUnavailable,
				Status: httpx.HTTPStatus(codeDBUnavailable),
				Reason: "数据库连接层失败（D8-C / AC-FC-02）",
				cause:  err,
			}
		}
		return err
	}

	out := &Error{
		SQLState:   pgErr.Code,
		Constraint: pgErr.ConstraintName,
		Table:      pgErr.TableName,
		Column:     pgErr.ColumnName,
		cause:      err,
	}

	switch pgErr.Code {
	case sqlStateUniqueViolation:
		// 只有**登记过**的唯一约束才映射；未登记的一律保持"未映射"（= 缺陷），
		// 不允许"23505 一律 409"兜底 —— 那会把新约束的口径悄悄定成 409。
		if rule, ok := uniqueRules[pgErr.ConstraintName]; ok {
			applyRule(out, rule)
		}
	case sqlStateCheckViolation:
		if rule, ok := checkRules[pgErr.ConstraintName]; ok {
			applyRule(out, rule)
		} else if pgErr.ConstraintName == "" {
			// **23514 但没有约束名 ⇒ 这不是 CHECK 违反**，不映射（未映射 = HTTP 层 500 + 按缺陷排查）。
			//
			// 真实形态（真库实测，见 `pgxerr_matrix_db_test.go` 的 R-23514-NOPART）：
			// 往 `audit_logs` 写一条落在**没有分区**的月份 → PG 报
			// `23514: no partition of relation "audit_logs" found for row`（无 constraint name）。
			// 那是**部署缺陷**（分区预建窗口不够 / 月度任务没跑），不是用户入参问题：
			// 按默认给 400 等于告诉客户端"改参数重试"，而它改什么都没用。
			// CHECK 违反一定带约束名，所以"空约束名"是一条能安全区分的边界。
		} else {
			// CHECK 未逐个登记时一律 400：CHECK 违反几乎都是"入参不满足不变式"，
			// 与 500 相比 400 更接近事实，且不会把用户输入问题报成服务端故障。
			out.Code, out.Status = codeInvalidParameter, httpx.HTTPStatus(codeInvalidParameter)
			out.Reason = "CHECK 违反但约束名未登记 → 按入参不满足不变式处理（400）"
		}
	case sqlStateForeignKeyViolation:
		if rule, ok := fkRules[pgErr.ConstraintName]; ok {
			applyRule(out, rule)
		} else {
			out.Code, out.Status = codeInvalidParameter, httpx.HTTPStatus(codeInvalidParameter)
			out.Reason = "外键违反但约束名未登记 → 按被引用对象不存在处理（400）"
		}
	case sqlStateNotNullViolation:
		out.Code, out.Status = codeMissingParameter, httpx.HTTPStatus(codeMissingParameter)
		out.Reason = "NOT NULL 违反 → 必填参数缺失（400）"
	case sqlStateRestrictViolation:
		// forbid_mutation()（auth_events / audit_logs 只增表保护，数据模型 I5）。
		// 这不是用户错误，而是**代码试图篡改只增表** → 500 缺陷。
		out.Constraint = firstNonEmpty(out.Constraint, "forbid_mutation")
	case sqlStateInvalidTextRepr, sqlStateNumericOutOfRange:
		out.Code, out.Status = codeInvalidParameter, httpx.HTTPStatus(codeInvalidParameter)
		out.Reason = "参数类型/数值超出数据库类型域（22P02/22003）→ 400"
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		// 并发冲突：可重试，不给业务码（调用方按 Retryable 决定重试）。
	case sqlStateConnectionFailure, sqlStateTooManyConnections,
		sqlStateAdminShutdown, sqlStateCrashShutdown, sqlStateCannotConnectNow,
		sqlStateProtocolViolation, sqlStateSQLClientUnableToEstablish:
		// 连接/会话层失败一律 DB_UNAVAILABLE（D8-C）；`IsUnavailable` 与这里共用同一份
		// SQLSTATE 清单，改一处即可，别再抄第二份。
		out.Code, out.Status = codeDBUnavailable, httpx.HTTPStatus(codeDBUnavailable)
		out.Reason = "数据库连接层失败（D8-C / AC-FC-02）"
	case sqlStateUndefinedTable, sqlStateUndefinedColumn:
		// schema 漂移（迁移没跑 / 模型与迁移不一致）：缺陷，不映射成业务码。
	}
	return out
}

func applyRule(out *Error, rule constraintRule) {
	out.Code = rule.code
	out.Status = httpx.HTTPStatus(rule.code)
	out.Reason = rule.reason
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Retryable 报告该错误是否值得重试（序列化失败/死锁）。
//
// 只对 40001/40P01 返回 true：它们是**事务层**的瞬时冲突，重跑同一事务通常成功；
// 其余错误重试只会把同一个失败放大成多次失败。
func Retryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.SQLState == sqlStateSerializationFail || e.SQLState == sqlStateDeadlockDetected
}

// IsNotFound 报告该错误是否属于「被引用的对象不存在」（404 类）。
func IsNotFound(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Status == 404
}

// ConstraintOf 取出错误里的约束名（空串表示没有）。排障与日志用。
func ConstraintOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Constraint
	}
	return ""
}
