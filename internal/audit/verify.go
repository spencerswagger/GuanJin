package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件是**整链校验 Job 的内核**（AC-AUD-03/04/05/11）。
//
// 口径（D4-C）：单全局链 + 月分区锚点 + **每日**整链复核；Job 跑在 `cmd/guanjind` 进程内
// （**不新增二进制**——新增一个巡检进程等于多一份部署、监控与权限面）。
//
// 两个指标（名字写死，S11 与监控面板按此对账）：
//   - `audit_chain_broken_total`：累计"发现断链次数"（每次 RunOnce 发现断点 +1，不按条累计）；
//   - `audit_anchor_age_seconds`：最近一次成功整链复核距今秒数（超阈值告警由监控侧按此值判断）。
//
// 为什么"每次 +1"而不是"每条 +1"：同一处断链每天都会被再发现一次，按条累计会让这个数字
// 无限膨胀且失去"故障次数"语义；运维要看的是"断过几次"。

var (
	chainBrokenTotal    atomic.Int64
	lastVerifiedUnixSec atomic.Int64
)

// ChainBrokenTotal 返回累计发现的断链次数（指标 `audit_chain_broken_total`）。
func ChainBrokenTotal() int64 { return chainBrokenTotal.Load() }

// AnchorAge 返回最近一次成功整链复核距今的时长（指标 `audit_anchor_age_seconds`）。
//
// 从未成功复核过时返回 `(0, false)`——**不要**用一个巨大数字冒充，那会让面板分不清
// "刚启动还没跑"与"锚点真的超期"。
func AnchorAge(now time.Time) (time.Duration, bool) {
	last := lastVerifiedUnixSec.Load()
	if last == 0 {
		return 0, false
	}
	return now.Sub(time.Unix(last, 0)), true
}

// ResetMetricsForTest 只给测试用（生产不清零：清零会让"断过"的历史消失）。
func ResetMetricsForTest() {
	chainBrokenTotal.Store(0)
	lastVerifiedUnixSec.Store(0)
}

// VerifyResult 是一次整链复核的结果。
type VerifyResult struct {
	// Checked 本次复核的记录数。
	Checked int
	// Broken 第一个断点（nil = 整链通过）。
	Broken *BrokenPoint
	// AnchorUsed 本次复核用来钉住**段首** `prev_tag` 的那个值（链首为空）：
	// 正常是"链上真实前驱的 `integrity_tag`"；只有前驱分区已被 DROP 时才是上月锚点（断点锚）。
	AnchorUsed []byte
	// From / To 复核区间（左闭右开，按自然月）。
	From, To time.Time
}

// Verifier 是整链复核器（无本地状态：多副本各自跑，结论一致）。
type Verifier struct {
	chain   *Chain
	db      *gorm.DB
	anchors *AnchorStore
	logger  *slog.Logger
	now     func() time.Time
}

// NewVerifier 装配校验器。
func NewVerifier(chain *Chain, db *gorm.DB, anchors *AnchorStore, logger *slog.Logger) (*Verifier, error) {
	if chain == nil {
		return nil, errors.New("audit: verifier requires chain")
	}
	if db == nil {
		return nil, errors.New("audit: verifier requires db")
	}
	if anchors == nil {
		return nil, errors.New("audit: verifier requires anchor store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Verifier{chain: chain, db: db, anchors: anchors, logger: logger, now: time.Now}, nil
}

// monthOf 把时间截到月初（UTC）——链按自然月分段。
func monthOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// RunOnce 复核**当月**的链（跨月分段：每段以该月锚点为首条的 `prev_tag`）。
//
// 只复核当月是刻意的：历史月份一旦封版，锚点就是它的"封条"，重跑历史段既慢又无新信息；
// 真要复核历史，用 `RunRange` 显式指定区间（运维/取证场景）。
func (v *Verifier) RunOnce(ctx context.Context) (VerifyResult, error) {
	now := v.now().UTC()
	from := monthOf(now)
	return v.RunRange(ctx, from, from.AddDate(0, 1, 0))
}

// RunRange 复核 `[from, to)` 区间。
//
// ## 锚点 / 段首口径（2026-11-11 修订：段首改为"真实前驱"，锚点退回"封条 + 断点兜底"）
//
// **段首 `prev_tag` 的唯一期望值 = 链上紧邻本段之前那条记录的 `integrity_tag`**
// （D4-C(2)「跨分区首条的 `prev_tag` 取上一分区末 tag」的逐字落地）。校验器用一次
// `TailTagBefore` 现查这个值，**不再读上月锚点当期望值**。
//
// 为什么必须换掉"用上月锚点当段首期望值"（这是 `audit_chain_broken_total` 每天假 +1 的根因）：
//   - 锚点是 `StartJob` **每一拍**拍下的快照（`AnchorCurrentMonth` 写"当月链尾 tag"），
//     而审计事实是持续的：**月末最后一拍之后再写一条**，上月锚点就落后于上月真实链尾；
//   - 次月首条记录的 `prev_tag` 指向的是那条"封锚之后写的"记录 → 与上月锚点不等
//     → 整个次月**每一拍**都报 `prev_tag_mismatch`（`audit_anchor_age` 停住、真断链被噪声淹没）。
//     实测复现见 `TestAnchorHeadUsesRealPredecessor`（`anchor_head_serial_db_test.go`）。
//
// 换掉之后判据反而更强：上月尾部被删 → 本段首条 `prev_tag` 指向**已不存在的 tag**
// → 与真实前驱不等 → 照样报 `prev_tag_mismatch`（不再有"快照滞后"这个假阳性来源）。
//
// 锚点仍管两件事，语义各自独立：
//   - **封条（段尾）**：`anchor[M]` = 第 M 月某次复核时的链尾 tag（`AnchorCurrentMonth` 写）。
//     **只做存在性判定**：整月区间内必须还能找到这条记录，否则 `anchor_mismatch`
//     （整月尾部/整月被清空时链内仍自洽，只有封条能发现）。
//   - **断点锚（段首兜底）**：仅当"前驱所在分区已被 DROP"（D4-C(2) 的 365 天归档，
//     M1 未启用）时，才用 `anchor[M-1]` 顶上段首期望值。
//
// 缺段首期望值（既无前驱、也无断点锚）：只有"该段就是链的首段"才可判（应为 nil）；
// 否则段首 `prev_tag` 属**未知**，不判（`VerifyFrom(..., checkFirst=false)`）。
func (v *Verifier) RunRange(ctx context.Context, from, to time.Time) (VerifyResult, error) {
	res := VerifyResult{From: from.UTC(), To: to.UTC()}
	month := monthOf(from)

	// ① 本月封条（段尾用；可能为空 = 该月还没封锚）。
	selfAnchor, err := v.anchors.Get(ctx, month)
	if err != nil {
		return res, fmt.Errorf("audit: load anchor: %w", err)
	}

	records, err := v.chain.Load(ctx, v.db, from, to)
	if err != nil {
		return res, err
	}
	res.Checked = len(records)

	// ② 段首期望值 = **真实前驱**的 tag（口径见函数注释）。
	var headAnchor []byte
	hasHead := false
	if len(records) > 0 {
		tag, found, err := v.chain.TailTagBefore(ctx, v.db, from, to)
		if err != nil {
			return res, err
		}
		if found {
			headAnchor, hasHead = tag, true
		}
	}
	if !hasHead {
		// ②′ 兜底 = 断点锚（上月锚点）。只在"前驱分区已被 DROP"时才会走到，
		// M1 不做归档删除，因此这里通常取不到值。
		prevAnchor, err := v.anchors.Get(ctx, month.AddDate(0, -1, 0))
		if err != nil {
			return res, fmt.Errorf("audit: load prev-month anchor: %w", err)
		}
		if len(prevAnchor) > 0 {
			headAnchor, hasHead = prevAnchor, true
		}
	}
	res.AnchorUsed = headAnchor

	// ③ 段首是否可判：有前驱/断点锚 → 可判；否则只有"该段是链首"才可判（应为 nil）。
	checkFirst := hasHead
	if !checkFirst && len(records) > 0 {
		var before int64
		// `EXISTS … LIMIT 1` 而不是 `count(*)`：只需回答"之前有没有"，精确计数在
		// `audit_logs`（按月分区、`occurred_at` 无单列索引）上要扫全表。
		if err := v.db.WithContext(ctx).Raw(
			`SELECT CASE WHEN EXISTS (SELECT 1 FROM audit_logs WHERE occurred_at < ?) THEN 1 ELSE 0 END`,
			from.UTC()).Scan(&before).Error; err != nil {
			return res, fmt.Errorf("audit: probe records before: %w", err)
		}
		checkFirst = before == 0
	}

	broken, err := v.chain.VerifyFrom(records, headAnchor, checkFirst)
	if err != nil {
		return res, err
	}
	// ④ 本月封条必须**在段内找得到对应记录**（若已封锚且复核的是整月区间）。
	//
	// 这里必须是**存在性**判定，不能拿"段尾 tag == 锚点"做等值判定（Owl II P0）：
	// `StartJob` 每一拍是「先 RunOnce 复核当月 → 再 AnchorCurrentMonth 写当月链尾」，
	// 所以 `anchor[M]` 是**上一次**复核时的链尾；同月之后只要再写一条审计事实，
	// 段尾就变了 → 等值判定会把"正常追加"报成 `integrity_tag_mismatch`，
	// 与本次要修的假告警症状一模一样（`chain_broken_total` 照样每天 +1、`anchor_age` 停住）。
	// 存在性判定既能发现"整月尾部被删"（锚点对应的那条没了），又不会误报正常追加。
	//
	// 只在**整月区间**（`to == 下月 1 号`）启用：`RunRange` 也支持取证用的子区间，
	// 那种区间里本来就不含封条对应的记录。
	if broken == nil && len(selfAnchor) > 0 && to.UTC().Equal(month.AddDate(0, 1, 0)) {
		if len(records) == 0 {
			// 已封锚、整月却一条不剩：这是尾部截断的**最极端形态**（整月被清空）。
			// `AnchorCurrentMonth` 对空月不写锚，所以"锚在、记录为零"必然是异常，
			// 不能走"没记录就不判"（那会把清空整月显示成"刚验过"，`anchor_age` 还会被刷新）。
			broken = &BrokenPoint{Index: 0, RecordID: uuid.Nil, Reason: ReasonAnchorMismatch}
		} else {
			found := false
			for i := range records {
				if bytesEqual(records[i].IntegrityTag, selfAnchor) {
					found = true
					break
				}
			}
			if !found {
				// 指到段内最后一条（便于人查"从哪之后没了"），但原因码单列 `anchor_mismatch`，
				// 别让运维以为最后那条记录的内容被改过。
				last := records[len(records)-1]
				broken = &BrokenPoint{Index: len(records) - 1, RecordID: last.ID, Reason: ReasonAnchorMismatch}
			}
		}
	}
	res.Broken = broken

	if broken != nil {
		chainBrokenTotal.Add(1)
		v.logger.Error("audit: chain broken",
			"record_id", broken.RecordID.String(), "index", broken.Index, "reason", broken.Reason,
			"from", from.UTC().Format(time.RFC3339), "to", to.UTC().Format(time.RFC3339))
		return res, nil
	}
	// 整链通过：刷新"最近成功复核时刻"（指标 `audit_anchor_age_seconds` 的基准）。
	lastVerifiedUnixSec.Store(v.now().UTC().Unix())
	v.logger.Info("audit: chain verified", "checked", res.Checked,
		"from", from.UTC().Format(time.RFC3339), "to", to.UTC().Format(time.RFC3339))
	return res, nil
}

// AnchorCurrentMonth 把"当月链尾"写成锚点（供 Job 在整链通过后调用）。
//
// 写锚点的意义：它是**封条**——整月尾部（或整月）被删时，链内每一对 prev/tag 仍然自洽，
// 只有"封条在本段里再也找不到对应记录"能发现。它**不再**充当下一段段首的期望值
// （那个值改由真实前驱提供，见 `RunRange` 的口径注）。
func (v *Verifier) AnchorCurrentMonth(ctx context.Context) error {
	now := v.now().UTC()
	from := monthOf(now)
	records, err := v.chain.Load(ctx, v.db, from, from.AddDate(0, 1, 0))
	if err != nil {
		return err
	}
	if len(records) == 0 {
		// 当月还没有审计事实：不写锚点（写一个空锚会让"空月"与"锚点丢失"混淆）。
		return nil
	}
	last := records[len(records)-1]
	return v.anchors.Put(ctx, from, last.IntegrityTag)
}

// DefaultVerifyInterval 是整链复核的默认周期（D4-C：每日）。
//
// 写成常量而不是配置键：D5 的 runtime_config 键是封闭集合，加一个键就是新增契约面。
const DefaultVerifyInterval = 24 * time.Hour

// StartJob 在**当前进程内**起一个后台复核任务（`cmd/guanjind` 调用；不新增二进制）。
//
// 首个回合延迟 `DefaultVerifyInterval`：启动期已经做过 DB/策略自检，再立刻跑一遍整链
// 会拖慢启动；若运维要"启动即校验"，用 `RunOnce` 显式调一次（测试也走这条路）。
func StartJob(ctx context.Context, v *Verifier, interval time.Duration, logger *slog.Logger) func() {
	if interval <= 0 {
		interval = DefaultVerifyInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	jobCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				if _, err := v.RunOnce(jobCtx); err != nil {
					logger.Warn("audit: verify job failed", "error", err)
					continue
				}
				if err := v.AnchorCurrentMonth(jobCtx); err != nil {
					logger.Warn("audit: anchor write failed", "error", err)
				}
			}
		}
	}()
	return cancel
}
