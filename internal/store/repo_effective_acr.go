package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件是 **`AC-FLOW-09`** 的读取面：无 `client_id` 上下文的自助操作，
// 其"生效配置 ACR 前置"要按**用户访问过的业务系统的最高档位**来定。
//
// 取法（索引 §2 #4 / 接口清单 :26、:307）：
//
//	max tier = user_visited_clients ⨝ clients(status='active') 的 tier 最大值
//	门槛     = login_policies(scope='tier_default', tier=max tier).min_acr
//
// 四条边界（`AC-FLOW-09` 判定口径）：
//   - **无 client 上下文 ⇒ 不取任何 `client_override`**（override 是按 client 生效的，
//     这里根本没有 client，拿它就是"猜一个业务系统的策略"）；
//   - tier 单值（L1/L2/L3 三值取 max，无并列歧义）；
//   - 无访问记录、或关联 client 全非 active → 回退**平台默认 L2**（索引 §2 #8）；
//   - client 被删/停用按 join 过滤即可，**不加** `tier_at_visit` 列（那是"记录时的档位"，
//     拿它算门槛会把"已停用/已降档"的业务系统算进来）。
//
// 为什么不在 SQL 里做 `CASE` 排序：L1/L2/L3 的顺序是**领域知识**，写在 SQL 里会在
// 第二个调用点被抄错；这里用显式排名表，档位顺序只有一处。

// PlatformDefaultTier 是无访问记录时回退的平台默认档位（索引 §2 #8：L2 → AAL1）。
const PlatformDefaultTier = "L2"

// tierRank 把档位折成可比的数字（开放集合只说这三档）。
func tierRank(tier string) int {
	switch tier {
	case "L1":
		return 1
	case "L2":
		return 2
	case "L3":
		return 3
	default:
		return 0
	}
}

// EffectiveACRRepo 提供"访问过的最高档位"与"档位默认门槛"两个只读能力。
type EffectiveACRRepo struct{ db *gorm.DB }

// NewEffectiveACRRepo 绑定连接（db 为 nil 属装配缺陷）。
func NewEffectiveACRRepo(db *gorm.DB) (*EffectiveACRRepo, error) {
	if db == nil {
		return nil, errors.New("store: effective ACR repo requires db")
	}
	return &EffectiveACRRepo{db: db}, nil
}

// MaxVisitedTier 取该用户访问过的、**仍 active** 的业务系统里的最高档位。
//
// found=false 表示没有任何可用访问记录（调用方按平台默认 L2 处理）。
func (r *EffectiveACRRepo) MaxVisitedTier(ctx context.Context, userID uuid.UUID) (tier string, found bool, err error) {
	if userID == uuid.Nil {
		return "", false, nil
	}
	var tiers []string
	err = r.db.WithContext(ctx).Raw(
		`SELECT c.tier
		   FROM user_visited_clients v
		   JOIN clients c ON c.client_id = v.client_id
		  WHERE v.user_id = ? AND c.status = 'active'`, userID).Scan(&tiers).Error
	if err != nil {
		return "", false, MapError(err)
	}
	if len(tiers) == 0 {
		return "", false, nil
	}
	best := ""
	for _, t := range tiers {
		if tierRank(t) > tierRank(best) {
			best = t
		}
	}
	if best == "" {
		// 出现了三档之外的取值（DB 有 CHECK，走到这里说明数据被绕过约束写入）：按缺陷报错，
		// 不静默回退——否则"档位写坏了"会被当成"没有访问记录"，门槛悄悄降级。
		return "", false, fmt.Errorf("%w: tier=%q", ErrTierDefaultSeed, tiers[0])
	}
	return best, true, nil
}

// TierDefaultMinACR 读某档位的默认门槛（`login_policies` 的 `tier_default` 行）。
//
// **只读 tier_default，不读 client_override**：这是 `AC-FLOW-09` 口径的一半
// （无 client 上下文时 override 不存在）。
func (r *EffectiveACRRepo) TierDefaultMinACR(ctx context.Context, tier string) (string, error) {
	if tierRank(tier) == 0 {
		return "", fmt.Errorf("%w: unknown tier %q", ErrTierDefaultSeed, tier)
	}
	var minACR string
	err := r.db.WithContext(ctx).Raw(
		`SELECT min_acr FROM login_policies WHERE scope = 'tier_default' AND tier = ?`, tier).Scan(&minACR).Error
	if err != nil {
		return "", MapError(err)
	}
	if minACR == "" {
		return "", fmt.Errorf("%w: tier_default tier=%q has no min_acr", ErrTierDefaultSeed, tier)
	}
	return minACR, nil
}
