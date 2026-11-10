package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件是哈希链的**落库与扫读**（AC-AUD-02/03/04/05/11 的"真库"那一半）。
//
// 口径（数据模型 §3.6 / D4-C / 索引 §4.2-19）：
//   - **单全局链**：写之前用 `pg_advisory_xact_lock` 串行化链尾读取与插入——
//     没有这把锁，两个并发事务会读到同一个 `prev_tag`，写出的两条记录**互为兄弟**，
//     链从那一刻起就断了（而这在事后只能被"整链复核"发现，不会报错）；
//   - 锁在**事务内**（`xact` 变体）：事务提交/回滚时自动释放，不依赖 defer 解锁；
//   - 锚点写 `runtime_config`：`audit.chain.anchor.<YYYYMM>`（值 = base64 的 tag）。
//     锚点让"删掉整月最后几条"也能被发现——否则尾部截断在链内看不出来。

// chainLockKey 是 `pg_advisory_xact_lock` 的键（任意固定大整数即可；
// 换它等于让新旧进程用两把锁，是**静默断链**级别的错误，故写成常量并加注释）。
const chainLockKey int64 = 2026111001

// ErrOccurredAtTooNew 表示 `occurred_at` 太靠未来（超过"链尾月份 + 1 个月"）。
//
// 为什么要上界（Owl II P2 ①）：链按自然月分段、每月复核后封锚。若允许随意往未来月写，
// 那个未来月会在**被封锚之前**就掺入记录，等它被复核时锚点与实际链尾对不上，
// 表现为"已封锚段不连续/锚点不符"，与真断链混在一起。界取"链尾月 + 1"：允许自然的跨月滚动，
// 但一次只准滚一个月。
var ErrOccurredAtTooNew = errors.New("audit: occurred_at is beyond the next month after chain tail")

// ErrOccurredAtTooOld 表示 `occurred_at` 早于链尾所在分区月初（会插进上个月分区、破链）。
var ErrOccurredAtTooOld = errors.New("audit: occurred_at predates the tail partition month")

// ErrEmptyRecord 表示待写记录缺必填字段。
var ErrEmptyRecord = errors.New("audit: record requires action, result and occurred_at")

// Append 在**一个事务内**追加一条记录：取链尾 → 算 tag → 插入。
//
// 返回写入后的 `IntegrityTag`（调用方可把它当锚点或拿去做后续链）。
// 同一事务内可以连续追加多条（`prev` 从库里读，天然接得上）。
func (c *Chain) Append(ctx context.Context, db *gorm.DB, rec Record) ([]byte, error) {
	if db == nil {
		return nil, errors.New("audit: nil db")
	}
	if c == nil || c.key.IsZero() {
		return nil, errors.New("audit: nil chain")
	}
	if rec.Action == "" || rec.Result == "" || rec.OccurredAt.IsZero() {
		return nil, ErrEmptyRecord
	}
	if rec.ID == uuid.Nil {
		id, err := NewID()
		if err != nil {
			return nil, err
		}
		rec.ID = id
	}

	var tag []byte
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// ① 串行化链尾（事务级 advisory lock）。`disableChainLock` 仅测试反证用。
		if !c.disableChainLock {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, chainLockKey).Error; err != nil {
				return fmt.Errorf("audit: take chain lock: %w", err)
			}
		}
		// ② 读链尾：按 (occurred_at, id) 的最后一条（与校验同序）。
		var (
			prev     []byte
			tailTime *time.Time
		)
		var tail struct {
			IntegrityTag []byte
			OccurredAt   time.Time
			CreatedAt    time.Time
		}
		// **防分叉的唯一保证是上面那把 advisory 锁**（实测：只摘掉它、保留 `FOR UPDATE`
		// 仍会出分叉 `dup_prev_tag=5/4`、`broken=true`——行锁挡不住"两个写者同时把同一条
		// 当链尾"，因为行锁只在同一条被并发锁住时生效，读到的可能都是同一条已提交链尾）。
		// `FOR UPDATE` 只是**冗余保险**：防"将来有人删掉 advisory 锁"或连接池/隔离级别变化。
		// `FOR UPDATE` 说明（冗余保险，不是保证）：
		// advisory 锁只保证"同一时刻只有一个写者进这段代码"，一旦锁的语义被绕过
		// （换连接池模式、事务隔离级别变化、或将来有人删掉那一行），行锁仍会挡住
		// "两个写者同时把同一条当链尾"。锁序固定为 **先 advisory、后行锁**，避免与
		// `EnsurePartitions` 的 "advisory → ACCESS EXCLUSIVE" 形成交叉死锁。
		tailSQL := `SELECT integrity_tag, occurred_at, created_at FROM audit_logs
			ORDER BY created_at DESC, id DESC LIMIT 1`
		if !c.disableRowLock {
			tailSQL += ` FOR UPDATE` // 第二道防线（Gate 反证时由测试摘除）
		}
		res := tx.Raw(tailSQL).Scan(&tail)
		if res.Error != nil && !isNoRows(res.Error) {
			return fmt.Errorf("audit: read chain tail: %w", res.Error)
		}
		// **仅测试用的注入点**：让"读链尾 → 插入"的窗口可控地张开，
		// 用来证明防线（advisory 锁 + 行锁）真的挡住了并发分叉，而不是"跑几次没撞上"。
		// 生产环境该字段恒为 nil，不产生任何行为。
		if c.afterTailRead != nil {
			c.afterTailRead()
		}
		if res.RowsAffected > 0 && len(tail.IntegrityTag) > 0 {
			prev = tail.IntegrityTag
			at := tail.OccurredAt
			tailTime = &at
		}

		// ②′ **链序 = 平台写入序，用 `created_at`**（Spec 裁定，2026-11-10）：
		// `occurred_at` 是**取证时间**——它是分区键、进主键、四个取证索引全按它降序，
		// 而且 `audit_logs_append_only` 触发器让写入后**永不可改**。抬高它等于永久改错
		// 取证时间、事后连原值都补不回来；而"并发下每条必须排在链尾之后"只需要一个
		// **平台自己的**单调量，`created_at` 正是（同表已有列，零 schema 变更）。
		//
		// 因此：`created_at` 抬到"链尾 + 1µs"，`occurred_at` **原样入库并参与 `integrity_tag`**
		// （哈希覆盖原值，才是"证据原样"）。
		// ②‴ 上界守卫：`occurred_at` 不得晚于"链尾月份 + 1 个月"（链为空时取当月 + 1）。
		{
			// 链空时：允许写到"当月 + 1"个月（含），所以上界取"当月 + 2"的开区间。
			limit := monthOf(time.Now().UTC()).AddDate(0, 2, 0)
			if tailTime != nil {
				limit = monthOf(*tailTime).AddDate(0, 2, 0) // 链尾月 + 1 个月（右开）
			}
			if !rec.OccurredAt.UTC().Before(limit) {
				return fmt.Errorf("%w: occurred_at %s >= %s",
					ErrOccurredAtTooNew, rec.OccurredAt.UTC().Format(time.RFC3339), limit.Format("2006-01"))
			}
		}
		if tailTime != nil {
			// ②″ 下界守卫：`occurred_at` 早于链尾所在分区月初 → 拒绝并留痕。
			// 否则今天的写入会插进上个月的分区，跨分区的 `prev_tag` 链接（AC-AUD-06）就断了。
			if floor := monthOf(*tailTime); rec.OccurredAt.Before(floor) {
				return fmt.Errorf("%w: occurred_at %s is before the tail partition month %s",
					ErrOccurredAtTooOld, rec.OccurredAt.UTC().Format(time.RFC3339), floor.Format("2006-01"))
			}
		}
		createdAt := time.Now().UTC()
		if tailTime != nil && !createdAt.After(tail.CreatedAt) {
			createdAt = tail.CreatedAt.Add(time.Microsecond)
		}
		// ③ 算 tag。
		computed, err := c.Tag(prev, rec)
		if err != nil {
			return err
		}
		// ④ 插入（两侧 tag 都落库；`signature` 可为空）。
		before, err := jsonOrNil(rec.Before)
		if err != nil {
			return err
		}
		after, err := jsonOrNil(rec.After)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO audit_logs
			(id, actor_user_id, actor_role, action, target_type, target_id, client_id,
			 before, after, result, integrity_tag, prev_tag, signature, ip, trace_id, occurred_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.ID, nullUUID(rec.ActorUserID), nullStr(rec.ActorRole), rec.Action,
			nullStr(rec.TargetType), nullStr(rec.TargetID), nullStr(rec.ClientID),
			before, after, rec.Result, computed, prev, rec.Signature,
			nullStr(rec.IP), nullStr(rec.TraceID), rec.OccurredAt.UTC(), createdAt).Error; err != nil {
			return fmt.Errorf("audit: insert record: %w", err)
		}
		tag = computed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tag, nil
}

// Load 按 `(occurred_at, id)` 升序读出区间内的记录（校验用）。
//
// 区间左闭右开：`[from, to)`——按自然月切片时不会重复/漏一条。
func (c *Chain) Load(ctx context.Context, db *gorm.DB, from, to time.Time) ([]Record, error) {
	if db == nil {
		return nil, errors.New("audit: nil db")
	}
	var rows []struct {
		ID           uuid.UUID
		ActorUserID  *uuid.UUID
		ActorRole    *string
		Action       string
		TargetType   *string
		TargetID     *string
		ClientID     *string
		Before       []byte
		After        []byte
		Result       string
		IntegrityTag []byte
		PrevTag      []byte
		Signature    []byte
		IP           *string
		TraceID      *string
		OccurredAt   time.Time
	}
	err := db.WithContext(ctx).Raw(`SELECT id, actor_user_id, actor_role, action, target_type, target_id,
			client_id, before, after, result, integrity_tag, prev_tag, signature, ip, trace_id, occurred_at
		FROM audit_logs WHERE occurred_at >= ? AND occurred_at < ?
		ORDER BY created_at ASC, id ASC`, from.UTC(), to.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("audit: load chain: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec := Record{
			ID: r.ID, ActorUserID: r.ActorUserID, Action: r.Action, Result: r.Result,
			Before: decodeJSON(r.Before), After: decodeJSON(r.After),
			OccurredAt: r.OccurredAt.UTC(),
			PrevTag:    r.PrevTag, IntegrityTag: r.IntegrityTag, Signature: r.Signature,
		}
		rec.ActorRole = deref(r.ActorRole)
		rec.TargetType = deref(r.TargetType)
		rec.TargetID = deref(r.TargetID)
		rec.ClientID = deref(r.ClientID)
		rec.IP = deref(r.IP)
		rec.TraceID = deref(r.TraceID)
		out = append(out, rec)
	}
	return out, nil
}

// AnchorStore 读写链锚点（`runtime_config`）。
type AnchorStore struct{ db *gorm.DB }

// NewAnchorStore 绑定连接。
func NewAnchorStore(db *gorm.DB) *AnchorStore { return &AnchorStore{db: db} }

// Put 写某月的锚点（base64 编码的 tag）。
func (s *AnchorStore) Put(ctx context.Context, month time.Time, tag []byte) error {
	if s == nil || s.db == nil {
		return errors.New("audit: nil anchor store")
	}
	encoded, err := json.Marshal(base64.StdEncoding.EncodeToString(tag))
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO runtime_config (key, value, value_type, description)
		 VALUES (?, ?::jsonb, 'string', '审计链月度锚点（D4-C）')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		AnchorKey(month), string(encoded)).Error
}

// Get 读某月的锚点；没有锚点返回 `(nil, nil)`（首月尚未写锚是合法状态）。
func (s *AnchorStore) Get(ctx context.Context, month time.Time) ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("audit: nil anchor store")
	}
	// `value #>> '{}'` 把 jsonb 的字符串取出为 text（去引号），所以直接按 string 扫。
	var encoded string
	res := s.db.WithContext(ctx).Raw(
		`SELECT value #>> '{}' FROM runtime_config WHERE key = ?`, AnchorKey(month)).Scan(&encoded)
	if res.Error != nil {
		return nil, res.Error
	}
	if encoded == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(encoded)
}

// 小工具（空值处理集中在这里，别散落在 SQL 参数里）。
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func jsonOrNil(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_raw": string(raw)}
	}
	return m
}

func isNoRows(err error) bool {
	return err != nil && (errors.Is(err, gorm.ErrRecordNotFound) || err.Error() == "sql: no rows in result set")
}

// EnsurePartitions 幂等建好给定月份的分区。
//
// 调用方两处：**生产** 由 `cmd/guanjind` 的 `startAuditPartitionJob`（启动即跑一次 + 每日 ticker，
// 预建当月+未来 5 个月）调用；**测试** 由 `internal/audit` 的 `TestMain` 调用。
// 生产侧不在这里再包事务/锁——本函数自己会在一个事务内先取链锁。
//
// **必须在同一事务内先取链锁**（`pg_advisory_xact_lock(chainLockKey)`）：
// `CREATE TABLE ... PARTITION OF` 要对父表取 ACCESS EXCLUSIVE，而任何写入
// （`Chain.Append`）都持有 ROW EXCLUSIVE 并先取同一把 advisory 锁——
// 不取锁就建分区，会在多包并行（`-p 8`）下与写入撞出 **40P01 死锁**
// （实测：`create audit_logs_p203606: ERROR: deadlock detected`）。
// 取了同一把锁，DDL 与写入就严格串行，且建表是幂等的（`IF NOT EXISTS`）。
func EnsurePartitions(ctx context.Context, db *gorm.DB, months []time.Time) error {
	if db == nil {
		return errors.New("audit: nil db")
	}
	if len(months) == 0 {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, chainLockKey).Error; err != nil {
			return fmt.Errorf("audit: partition lock: %w", err)
		}
		for _, month := range months {
			name := fmt.Sprintf("audit_logs_p%s", month.UTC().Format("200601"))
			from := month.UTC().Format("2006-01-02")
			to := month.UTC().AddDate(0, 1, 0).Format("2006-01-02")
			if err := tx.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF audit_logs
				FOR VALUES FROM ('%s') TO ('%s')`, name, from, to)).Error; err != nil {
				return fmt.Errorf("audit: create partition %s: %w", name, err)
			}
		}
		return nil
	})
}

// DropPartitions 幂等删除给定月份的分区（同样先取链锁，理由同上）。
func DropPartitions(ctx context.Context, db *gorm.DB, months []time.Time) error {
	if db == nil {
		return errors.New("audit: nil db")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, chainLockKey).Error; err != nil {
			return fmt.Errorf("audit: partition lock: %w", err)
		}
		for _, month := range months {
			name := fmt.Sprintf("audit_logs_p%s", month.UTC().Format("200601"))
			if err := tx.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)).Error; err != nil {
				return fmt.Errorf("audit: drop partition %s: %w", name, err)
			}
		}
		return nil
	})
}
