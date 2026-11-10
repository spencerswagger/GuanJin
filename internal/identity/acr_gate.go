package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
)

// 本文件是 **`AC-FLOW-09`** 的判定层：**无 `client_id` 上下文**的自助操作，
// 其"生效配置 ACR 前置"按**该用户访问过的业务系统的最高档位**推导。
//
// 判据原文（`acceptance-criteria.md:140`）：访问过 L3 后，无上下文请求的 ACR 前置 = L3。
//
// 四条边界（与 Spec 裁定一一对应，见 `internal/store/repo_effective_acr.go` 文件头）：
//   - 无 client 上下文 ⇒ **不取任何 `client_override`**（这里没有 client，取它就是猜）；
//   - tier 三值取 max，单值无冲突；
//   - 无访问记录 / 关联 client 全非 active → 回退**平台默认 L2 → AAL1**；
//   - client 停用/删除按 join 过滤，不加 `tier_at_visit` 列。
//
// 为什么单独一层而不是塞进 handler：这条判定会被 `/me/*` 的其它自助操作复用
// （`POST /api/v1/me/password` 等都写着"旧口令/会话 ACR 判定"），放在 handler 里
// 迟早出现第二份"最高档位"的实现，而两份一定会有一天不一致。

// VisitedTierReader 是"最高访问档位"的能力面（实现是 `*store.EffectiveACRRepo`）。
type VisitedTierReader interface {
	// MaxVisitedTier 返回仍 active 的业务系统里的最高档位；found=false 表示无可用记录。
	MaxVisitedTier(ctx context.Context, userID uuid.UUID) (tier string, found bool, err error)
	// TierDefaultMinACR 读某档位的默认门槛（`login_policies.scope='tier_default'`）。
	TierDefaultMinACR(ctx context.Context, tier string) (string, error)
}

// EffectiveTarget 是"无 client 上下文"的生效配置 ACR 前置。
type EffectiveTarget struct {
	// Tier 参与判定的档位（可能是回退后的平台默认 L2）。
	Tier string
	// ACR 该档位的默认门槛（`tier_default.min_acr`）。
	ACR factor.ACR
	// Fallback=true 表示没有任何可用访问记录，走了平台默认档位。
	Fallback bool
}

// ACRGate 判定"无 client 上下文的自助操作"够不够门槛。
type ACRGate struct {
	visited VisitedTierReader
	// defaultTier 平台默认档位（索引 §2 #8：L2 → AAL1）。空值时取 `L2`。
	defaultTier string
}

// NewACRGate 装配判定器（缺依赖即装配期失败）。
func NewACRGate(visited VisitedTierReader, defaultTier string) (*ACRGate, error) {
	if visited == nil {
		return nil, errors.New("identity: acr gate requires visited tier reader")
	}
	if defaultTier == "" {
		defaultTier = "L2"
	}
	return &ACRGate{visited: visited, defaultTier: defaultTier}, nil
}

// Target 解析该用户在"无 client 上下文"下的目标 ACR。
func (g *ACRGate) Target(ctx context.Context, userID uuid.UUID) (EffectiveTarget, error) {
	tier, found, err := g.visited.MaxVisitedTier(ctx, userID)
	if err != nil {
		return EffectiveTarget{}, err
	}
	fallback := !found
	if fallback {
		tier = g.defaultTier
	}
	raw, err := g.visited.TierDefaultMinACR(ctx, tier)
	if err != nil {
		return EffectiveTarget{}, err
	}
	acr := factor.ACR(raw)
	if !acr.Issuable() {
		// 库里出现 AAL3（预留）或未知值：配置缺陷，不能当成"门槛很低"放行。
		return EffectiveTarget{}, fmt.Errorf("%w: tier=%s min_acr=%q", ErrInvalidACRConfig, tier, raw)
	}
	return EffectiveTarget{Tier: tier, ACR: acr, Fallback: fallback}, nil
}

// ErrInvalidACRConfig 表示 `tier_default.min_acr` 不是可签发级别（配置缺陷）。
var ErrInvalidACRConfig = errors.New("identity: tier_default.min_acr is not issuable")

// Check 判定"当前 ACR 是否达到目标"，不足时返回 `*ACRInsufficientError`。
//
// 只比较、不改任何状态：是否要开流补步由调用方决定（无上下文时根本开不出流——
// `flows.client_id NOT NULL`，这也是 403 不带 `details.flow` 的原因）。
func (g *ACRGate) Check(current factor.ACR, target EffectiveTarget) error {
	if current.AtLeast(target.ACR) {
		return nil
	}
	return &ACRInsufficientError{Current: current, Target: target.ACR, Tier: target.Tier}
}

// ACRInsufficientError 是"档位不足"的判定结果（对外 `ACR_INSUFFICIENT` / 403）。
//
// **不带 `details.flow`**：无 client 上下文时没有 flow 行可引用，硬给一个 `flow_id`
// 会让客户端去调 `/auth/challenge` 补步，而那条路需要 client——等于把人引到死路上。
// 详细口径见 D18 草案（待 spencer 拍板后转正式）。
type ACRInsufficientError struct {
	Current factor.ACR
	Target  factor.ACR
	Tier    string
}

func (e *ACRInsufficientError) Error() string {
	if e == nil {
		return "identity: acr insufficient"
	}
	return fmt.Sprintf("identity: acr insufficient (current=%s target=%s tier=%s)",
		e.Current, e.Target, e.Tier)
}
