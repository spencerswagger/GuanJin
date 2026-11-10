package store

import (
	"context"
	"database/sql"
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
//
// **并发正确性靠 advisory 事务锁**（不是靠行锁）：键不存在时 `FOR UPDATE` 锁不到行，
// 并发首次写入会各自读到空集 → 一次真实变更记两条审计 / 丢更新。详见
// `platformFactorLockKey` 的注释与 AC-FLOW-05 判据 4 的用例。

// KeyFactorsDisabled 是平台级因子启停的唯一存储位（`runtime_config`，jsonb 数组）。
const KeyFactorsDisabled = "factors.disabled"

// platformFactorLockKey 是"平台级因子启停"的 **advisory 事务锁**键（固定常量）。
//
// 为什么不能只靠 `SELECT … FOR UPDATE`（2026-11-11 缺陷根因）：行锁锁的是**行**，
// 键还不存在时 `FOR UPDATE` 一行都锁不到 —— 20 个并发首次 PATCH 各自读到"空集"，
// 于是同一次真实变更被记成**两条审计**（两条 `before=true`），enable/disable 交叉时还会丢更新。
// advisory 锁不依赖"行存在"，也不依赖 key 的形态（单行聚合键 / 逐因子键都一样成立）。
//
// 为什么是 advisory 而不是"先 INSERT 占位再 FOR UPDATE"：占位写会让"同值幂等"的请求也建行，
// 而且必须先写一个假值再读；advisory 锁零副作用、只在事务期持有。
//
// 多实例部署：`pg_advisory_xact_lock` 是**数据库级**的（所有后端共享），不是进程内锁，
// 所以多副本同时 PATCH 也串行。
//
// **锁序固定：先 factors 锁，再 runtime_config 行锁，最后审计链自己的 advisory 锁**
// （`audit.Chain.Append` 内部取 `chainLockKey`）。反向路径不存在，因此不会成环；
// 换这个键等于让新旧进程用两把锁 → **静默丢更新**级别的错误，故写成常量并加注释。
const platformFactorLockKey int64 = 2026111101

// 错误：装配期与输入期。
var (
	// ErrNilFactorAudit 表示没有注入审计写入器（平台级变更必须留痕，缺了就是配置缺陷）。
	ErrNilFactorAudit = errors.New("store: platform factor audit writer is required")
	// ErrFactorTypeRequired 表示没给因子类型。
	ErrFactorTypeRequired = errors.New("store: factor type is required")
	// ErrFactorDisabledStore 表示 `factors.disabled` 的值不是合法的字符串数组（配置被写坏）。
	ErrFactorDisabledStore = errors.New("store: factors.disabled is not a string array")
	// ErrFactorDisabledMissing 表示 `factors.disabled` **这一行不存在**（D22 要求它由迁移 seed 出来）。
	//
	// **为什么是错误而不是空集**（2026-11-11 定稿口径）：把"缺行"当空集是**安全方向的静默降级** ——
	// 平台禁用集读成空集，等于"该禁的因子全都可用"，而系统照常跑、只在日志里少一行。
	// 缺行的真实含义只有两种：迁移没跑 / 有人删了行；两种都必须当场炸（启动 fail-fast、运行期 500）。
	ErrFactorDisabledMissing = errors.New("store: runtime_config[" + KeyFactorsDisabled + "] row is missing")
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

	// afterRead 是**仅测试用**的注入点（生产恒为 nil）：在"读到当前集合"与"写回"之间插入
	// 可控延迟，用来把"行不存在 → 并发都读到空集"的窗口张开，证明互斥真的挡住了竞态
	// （与 `audit.Chain.afterTailRead` 同一手法：不靠"多跑几次碰运气"）。
	afterRead func()
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
// **缺行 → `ErrFactorDisabledMissing`**，值不是字符串数组（含 `null` / `{}`）→ `ErrFactorDisabledStore`；
// 两者都是 fail-closed 的错误，**不再**把任何一种静默当成空集（D22 定稿口径 2026-11-11）：
// "平台级禁用集为空"的唯一表达是**行存在且值为 `[]`**。
func (r *PlatformFactorReader) Disabled(ctx context.Context) ([]string, error) {
	var raw string
	row := r.db.WithContext(ctx).Raw(
		`SELECT value::text FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Row()
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFactorDisabledMissing
		}
		return nil, MapError(err)
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
		// ① **先取 advisory 事务锁**：行不存在时 `FOR UPDATE` 锁不到任何行（见常量注释），
		//    并发首次 PATCH 会各自读到空集 → 一次真实变更记两条审计 / 丢更新。
		//    这把锁与"行在不在"无关，且跨实例生效。
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, platformFactorLockKey).Error; err != nil {
			return fmt.Errorf("store: lock platform factors: %w", err)
		}
		// ② 再锁行：串行化"读集合 → 改 → 写回"。行已存在时它是第一道防线，
		//    行不存在时是空操作（真正的互斥由 ① 提供）——两道都留，防以后有人只删掉其中一道。
		var current string
		row := tx.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ? FOR UPDATE`,
			KeyFactorsDisabled).Row()
		if err := row.Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// 缺行 = 部署缺陷（D22 的 seed 没跑 / 被删）→ fail-closed：**不**顺手 insert 建行。
				// 早先"缺行按空集 + 写入时 upsert 建行"看起来更"健壮"，实际是两条安全降级：
				// ① 读成空集等于"该禁的因子全可用"；② 谁先写谁决定初始值，并发下还会丢更新。
				return ErrFactorDisabledMissing
			}
			return MapError(err)
		}

		if r.afterRead != nil {
			r.afterRead()
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

		// 只 UPDATE（不 upsert）：行必须由迁移 seed 出来（D22），缺行在 ② 就已经 fail-closed 返回。
		// `RowsAffected != 1` 说明行在 ② 与这里之间被删了（并发运维动作）→ 同样当缺行处理，不静默成功。
		up := tx.Exec(`UPDATE runtime_config
			   SET value = ?::jsonb, value_type = 'json', description = ?, updated_by = ?, updated_at = now()
			 WHERE "key" = ?`,
			string(payload), platformFactorConfigDesc, nullableUUID(in.ActorUserID), KeyFactorsDisabled)
		if up.Error != nil {
			return MapError(up.Error)
		}
		if up.RowsAffected != 1 {
			return ErrFactorDisabledMissing
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

// UnknownFactorTypes 返回 `disabled` 里**本进程注册表不认识**的类型（排序、去重）。
//
// 口径（2026-11-11 定稿）：未知类型**读保留原文、不删不丢**——滚动升级时新版本已加的类型，
// 旧节点不认识；一旦在读-改-写里把它丢掉，那个类型在其它节点上就会**意外变为可用**（安全降级）。
// 但必须**点名告警**：数组里出现未知类型（尤其把类型名写错）意味着"本该禁用的那个因子其实没被禁用"，
// 只留一行日志没人看，所以要带上全量集合一起 WARN。
func UnknownFactorTypes(disabled []string, known []string) []string {
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	seen := make(map[string]struct{}, len(disabled))
	out := make([]string, 0, len(disabled))
	for _, t := range disabled {
		if _, ok := knownSet[t]; ok {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// decodeDisabledFactors 把 jsonb 文本解成字符串集合（`null`/空串/非数组一律当坏值）。
func decodeDisabledFactors(raw string) ([]string, error) {
	if raw == "" || raw == "null" {
		// `null` 与"空集"不可区分（D22 原文），且 `jsonb 'null'` 展开会报错 → 一律当坏值。
		// "空集"只能用 `[]` 表达。
		return nil, fmt.Errorf("%w: value is %q (empty set must be [])", ErrFactorDisabledStore, raw)
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
