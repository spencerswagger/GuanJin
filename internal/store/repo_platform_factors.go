package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件是 **平台级因子启停**的存储位与写入路径（`AC-FLOW-05`）。
//
// 语义（接口清单 §3.8 factors 段):
//   - **平台级开关**与 `login_policies.disabled_factors`（客户端级）是两回事，**不互相覆盖**：
//     有效禁用 = 平台级 ∪ 客户端级（并集，任一级禁用即不可用）；
//   - 平台级状态落 **`runtime_config` 的 `factors.disabled`**（jsonb 字符串数组）：
//     零 schema 变更、零新表（照 `flow.ttl_seconds` / `challenge.*` 先例）；
//   - 每次真正的变更都要**在同一事务内**追加一条 `audit_logs`（`action=factor.disable|enable`），
//     审计写失败 **整体回滚**——否则会出现"关了但没留痕"或"留痕了但没关"。
//
// 为什么键不存在时不报错、而按空集处理：平台级禁用默认就该是"没有任何因子被停"；
// 首次写入时 `ON CONFLICT` 建行即可。这样新环境不需要额外的 seed 迁移（避免"忘了跑 seed
// 就启停不了"的隐性前置条件）。

// KeyFactorsDisabled 是平台级因子启停的唯一存储位（`runtime_config`，jsonb 数组）。
const KeyFactorsDisabled = "factors.disabled"

// 错误：装配期与输入期。
var (
	// ErrNilFactorAudit 表示没有注入审计写入器（平台级变更必须留痕，缺了就是配置缺陷）。
	ErrNilFactorAudit = errors.New("store: platform factor audit writer is required")
	// ErrFactorTypeRequired 表示没给因子类型。
	ErrFactorTypeRequired = errors.New("store: factor type is required")
	// ErrFactorDisabledStore 表示 `factors.disabled` 的值不是合法的字符串数组（配置被写坏）。
	ErrFactorDisabledStore = errors.New("store: factors.disabled is not a string array")
)

// FactorAuditRecord 是一条"平台级因子启停"的审计事实（字段口径见 D4-C）。
//
// 这里**不含** `integrity_tag` / `prev_tag`：那两段由审计链在**同一事务内**算出来
// （装配层把本结构翻成 `audit.Record` 再交给链）。存储层不碰链算法——否则会出现
// 第二份 canonical 编码，链一验就断（AC-AUD-03）。
type FactorAuditRecord struct {
	ID          uuid.UUID
	ActorUserID uuid.UUID
	ActorRole   string
	// Action 是 `factor.disable` / `factor.enable`（两种动作分开记，便于审计检索）。
	Action string
	// TargetType 固定 `factor`，TargetID 是因子类型。
	TargetType string
	TargetID   string
	Before     map[string]any
	After      map[string]any
	Result     string
	IP         string
	TraceID    string
	OccurredAt time.Time
}

// FactorAuditWriter 是"在调用方事务里追加一条审计事实"的窄接口（实现是审计链，见装配层）。
//
// **必须接受外部事务**：判据要求"改配置 + 写审计"原子生效/回滚，所以写入器不能自己开事务。
type FactorAuditWriter interface {
	AppendInTx(ctx context.Context, tx *gorm.DB, rec FactorAuditRecord) error
}

// SetFactorEnabledInput 是一次平台级启停的入参。
type SetFactorEnabledInput struct {
	Type string
	// Enabled=true 表示"启用该因子"（即从 disabled 集合里移除）。
	Enabled     bool
	Reason      string
	ActorUserID uuid.UUID
	ActorRole   string
	TraceID     string
	IP          string
	Now         time.Time
}

// PlatformFactorReader 只读 `factors.disabled`（编排层装配用：算"有效禁用"不需要写审计）。
type PlatformFactorReader struct{ db *gorm.DB }

// NewPlatformFactorReader 绑定连接（db 为 nil 属装配缺陷，直接报错）。
func NewPlatformFactorReader(db *gorm.DB) (*PlatformFactorReader, error) {
	if db == nil {
		return nil, errors.New("store: platform factor reader requires db")
	}
	return &PlatformFactorReader{db: db}, nil
}

// PlatformFactorRepo 读写 `factors.disabled`（平台级因子启停）。
type PlatformFactorRepo struct {
	*PlatformFactorReader
	audit FactorAuditWriter
}

// NewPlatformFactorRepo 绑定读侧与审计写入器；缺任一即装配期失败（不留 nil 分支）。
func NewPlatformFactorRepo(reader *PlatformFactorReader, audit FactorAuditWriter) (*PlatformFactorRepo, error) {
	if reader == nil || reader.db == nil {
		return nil, errors.New("store: platform factor repo requires reader")
	}
	if audit == nil {
		return nil, ErrNilFactorAudit
	}
	return &PlatformFactorRepo{PlatformFactorReader: reader, audit: audit}, nil
}

// Disabled 读平台级被禁用的因子集合（排序后返回，便于断言与稳定输出）。
//
// 键不存在 → 空集（不是错误，见文件头）；值不是字符串数组 → `ErrFactorDisabledStore`
// （配置被写坏属缺陷，不能静默当成空集——那会让"管理员关了却没生效"永远查不出来）。
func (r *PlatformFactorReader) Disabled(ctx context.Context) ([]string, error) {
	var raw string
	err := r.db.WithContext(ctx).Raw(
		`SELECT value::text FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Scan(&raw).Error
	if err != nil {
		return nil, MapError(err)
	}
	if raw == "" {
		return nil, nil
	}
	return decodeDisabledFactors(raw)
}

// SetEnabled 平台级启用/停用某因子：**一个事务内**改 `factors.disabled` + 追加 `audit_logs`。
//
// 幂等：集合没变化时**不写审计、不 UPDATE**（重复提交同一条启停不该产生第二条审计事实）。
// 失败：任何一步失败都回滚——包括审计写入失败（判据要求，见文件头）。
func (r *PlatformFactorRepo) SetEnabled(ctx context.Context, in SetFactorEnabledInput) error {
	if in.Type == "" {
		return ErrFactorTypeRequired
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 锁住这一行：并发两个 PATCH 时"读集合 → 改 → 写回"必须串行，
		// 否则会丢更新（后写的覆盖先写的，且审计里两条 before/after 都算错）。
		var current string
		row := tx.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ? FOR UPDATE`,
			KeyFactorsDisabled).Scan(&current)
		if row.Error != nil {
			return MapError(row.Error)
		}

		disabled, err := decodeDisabledFactors(current)
		if err != nil {
			return err
		}
		next, changed := toggleFactor(disabled, in.Type, in.Enabled)
		if !changed {
			return nil // 幂等：无变化 → 无审计、无写入
		}
		payload, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("store: marshal %s: %w", KeyFactorsDisabled, err)
		}

		// upsert：键不存在时建行（零 seed 迁移），存在时覆盖。
		// `value_type='json'`（DB CHECK 允许集合里），`is_secret=false`。
		up := tx.Exec(`INSERT INTO runtime_config ("key", value, value_type, description, is_secret, updated_by)
			VALUES (?, ?::jsonb, 'json', ?, false, ?)
			ON CONFLICT ("key") DO UPDATE
			   SET value = EXCLUDED.value, value_type = 'json', updated_by = EXCLUDED.updated_by`,
			KeyFactorsDisabled, string(payload), platformFactorConfigDesc, nullableUUID(in.ActorUserID))
		if up.Error != nil {
			return MapError(up.Error)
		}

		action := "factor.enable"
		if !in.Enabled {
			action = "factor.disable"
		}
		rec := FactorAuditRecord{
			ID:          newUUIDv7(),
			ActorUserID: in.ActorUserID,
			ActorRole:   in.ActorRole,
			Action:      action,
			TargetType:  "factor",
			TargetID:    in.Type,
			Before:      map[string]any{"enabled": !in.Enabled},
			After:       map[string]any{"enabled": in.Enabled, "reason": in.Reason},
			Result:      "success",
			IP:          in.IP,
			TraceID:     in.TraceID,
			OccurredAt:  now,
		}
		if err := r.audit.AppendInTx(ctx, tx, rec); err != nil {
			return fmt.Errorf("store: audit factor %s: %w", in.Type, err)
		}
		return nil
	})
}

const platformFactorConfigDesc = "平台级被禁用的因子类型（jsonb 字符串数组；与客户端级 disabled_factors 取并集）"

// PlatformDisabledFactors 是给编排层用的便捷读取：返回 set（`有效禁用` 的平台半边）。
func (r *PlatformFactorReader) PlatformDisabledFactors(ctx context.Context) (map[string]bool, error) {
	list, err := r.Disabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(list))
	for _, t := range list {
		out[t] = true
	}
	return out, nil
}

// decodeDisabledFactors 把 jsonb 文本解成字符串集合（undefined/null → 空）。
func decodeDisabledFactors(raw string) ([]string, error) {
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFactorDisabledStore, err)
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, t := range list {
		if t == "" {
			return nil, fmt.Errorf("%w: empty type", ErrFactorDisabledStore)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// toggleFactor 返回新集合与"是否变化"（排序输出，保证落库与响应稳定）。
func toggleFactor(disabled []string, typ string, enabled bool) ([]string, bool) {
	has := false
	for _, t := range disabled {
		if t == typ {
			has = true
			break
		}
	}
	switch {
	case enabled && !has:
		return disabled, false
	case !enabled && has:
		return disabled, false
	}
	next := make([]string, 0, len(disabled)+1)
	for _, t := range disabled {
		if t == typ {
			continue
		}
		next = append(next, t)
	}
	if !enabled {
		next = append(next, typ)
	}
	sort.Strings(next)
	return next, true
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// mustV7 生成 UUID v7（主键由应用层给，D4-A：迁移不写 DEFAULT uuidv7()）。
func newUUIDv7() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 只在熵源不可用时失败：那是进程级故障，panic 比"写出重复主键"好。
		panic("store: uuid v7: " + err.Error())
	}
	return id
}
