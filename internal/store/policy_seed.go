package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// 本文件是 decisions.md **D13** 的落地：`login_policies` 的三档 `tier_default`
// 必须**各恰一行**，且这件事要在 `cmd/guanjind` **装配期**判定 —— 缺失/多行 → 非 0 退出，
// 不允许拖到第一个请求上变成 500（那就是"配置缺陷伪装成服务故障"）。
//
// 为什么校验放在 store 而不是 main：查询与"三档是哪三档"都是数据层的事实，
// main 只该做「拿到 error 就退出」这一件事；把规则留在数据层，S6 的策略读取路径
// 与启动期校验才共用同一份口径，不会各写一套。

// TierDefaultTiers 是三档档位（顺序即档位顺序：错误信息、日志都按这个顺序出）。
//
// 出处：基线索引 §2「档位只有客户端档位一层」+ §4.2-15（L1/L2 → AAL1、L3 → AAL2）。
// **没有第四档、没有 AAL3**（D12：V1 只对外 AAL1/AAL2），所以这里是闭集，不是"参考列表"。
// 用数组而不是切片：读它的人拿到的是副本，改不动这份口径。
var TierDefaultTiers = [...]string{"L1", "L2", "L3"}

// ErrTierDefaultSeed 是「三档 tier_default 没有各恰一行」的根错误（`errors.Is` 可判）。
var ErrTierDefaultSeed = errors.New("store: login_policies tier_default seed violated")

// TierDefaultSeedError 把「哪一档缺、哪一档多」逐条摊开。
//
// 为什么不只返回一句字符串：启动失败的日志要能**直接对账**（缺 L3 还是 L2 多了一行），
// 否则运维还得再连库查一遍，而那时进程已经退了。Counts 是当时的计数快照，用来留证。
type TierDefaultSeedError struct {
	// Missing 是"该档一行都没有"的档位（按 TierDefaultTiers 顺序）。
	Missing []string
	// Duplicated 是"该档不止一行"的档位（唯一索引本应拦住，出现即 schema 漂移）。
	Duplicated []string
	// Unexpected 是"出现了三档之外的 tier 取值"（同样属 schema 漂移）。
	Unexpected []string
	// Counts 是实际计数（只含 scope='tier_default' 且 tier 非空的行）。
	Counts map[string]int
}

// Error 实现 error 接口：先给结论，再给逐档计数，最后点名异常档位。
func (e *TierDefaultSeedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("store: login_policies 的三档 tier_default 必须各恰一行（D13）")
	b.WriteString("，实际 ")
	for i, tier := range TierDefaultTiers {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%d", tier, e.Counts[tier])
	}
	if len(e.Missing) > 0 {
		b.WriteString("；缺失 " + strings.Join(e.Missing, ","))
	}
	if len(e.Duplicated) > 0 {
		b.WriteString("；多行 " + strings.Join(e.Duplicated, ","))
	}
	if len(e.Unexpected) > 0 {
		b.WriteString("；越界档位 " + strings.Join(e.Unexpected, ","))
	}
	return b.String()
}

// Unwrap 让调用方既能 `errors.Is(err, ErrTierDefaultSeed)`，也能取具体档位。
func (e *TierDefaultSeedError) Unwrap() error { return ErrTierDefaultSeed }

// ValidateTierDefaultCounts 是规则的**唯一实现**（纯函数，无 I/O）。
//
// 判定：`TierDefaultTiers` 每档恰好 1 行；出现三档之外的档位一律算失败（schema 漂移）。
// 刻意**不校验 `min_acr` 的取值**：D13 的幂等口径是「运维改过的值不被覆盖」，
// 也就是说取值允许被有意调整；把取值也钉死在启动期会把合规改动变成启动失败。
// 行数才是"策略是否存在"的事实。
func ValidateTierDefaultCounts(counts map[string]int) error {
	known := make(map[string]struct{}, len(TierDefaultTiers))
	for _, tier := range TierDefaultTiers {
		known[tier] = struct{}{}
	}

	snapshot := make(map[string]int, len(counts))
	var missing, duplicated, unexpected []string
	for _, tier := range TierDefaultTiers {
		n := counts[tier]
		snapshot[tier] = n
		switch {
		case n == 0:
			missing = append(missing, tier)
		case n > 1:
			duplicated = append(duplicated, tier)
		}
	}
	for tier, n := range counts {
		if _, ok := known[tier]; ok || n == 0 {
			continue
		}
		snapshot[tier] = n
		unexpected = append(unexpected, tier)
	}
	sort.Strings(unexpected)

	if len(missing) == 0 && len(duplicated) == 0 && len(unexpected) == 0 {
		return nil
	}
	return &TierDefaultSeedError{
		Missing:    missing,
		Duplicated: duplicated,
		Unexpected: unexpected,
		Counts:     snapshot,
	}
}

// VerifyTierDefaults 读真库并校验三档 tier_default 各恰一行（启动期 fail-fast 用）。
//
// 查询只数 `scope='tier_default'` 的行；`tier IS NOT NULL` 是防御性写法 ——
// DDL 的 `login_policies_scope_xor` CHECK 已保证 tier_default 的 tier 非空，
// 这里再写一遍是为了"schema 漂移时行为可预期"，不是重复校验。
func VerifyTierDefaults(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("store: nil db (cannot verify login_policies tier_default seed)")
	}

	var rows []struct {
		Tier  string `gorm:"column:tier"`
		Count int    `gorm:"column:count"`
	}
	res := db.WithContext(ctx).Raw(
		`SELECT tier, count(*) AS count
		   FROM login_policies
		  WHERE scope = 'tier_default' AND tier IS NOT NULL
		  GROUP BY tier`).Scan(&rows)
	if res.Error != nil {
		return MapError(res.Error)
	}

	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.Tier] = r.Count
	}
	return ValidateTierDefaultCounts(counts)
}
