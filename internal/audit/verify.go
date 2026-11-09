package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

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
	// AnchorUsed 本次复核使用的锚点（首月为空）。
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
func (v *Verifier) RunRange(ctx context.Context, from, to time.Time) (VerifyResult, error) {
	res := VerifyResult{From: from.UTC(), To: to.UTC()}

	anchor, err := v.anchors.Get(ctx, monthOf(from))
	if err != nil {
		// 读不到锚点 = 无法确认分段起点，**不许**退化成 nil 继续校验（那会把"锚点丢了"
		// 伪装成"首段正常"）。
		return res, fmt.Errorf("audit: load anchor: %w", err)
	}
	res.AnchorUsed = anchor

	records, err := v.chain.Load(ctx, v.db, from, to)
	if err != nil {
		return res, err
	}
	res.Checked = len(records)

	broken, err := v.chain.Verify(records, anchor)
	if err != nil {
		return res, err
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
// 写锚点的意义：下个月复核时以它为分段起点，且**尾部被整段删除**时能被发现
// （那时链内每一对 prev/tag 仍然自洽，只有锚点对不上）。
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
