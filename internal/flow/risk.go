package flow

import (
	"context"
	"errors"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
)

// 本文件是**风控与编排的交界**：账号级失败计数（Redis 权威，D3-D）在这里被调用，
// 但"怎么数"不在这里实现（实现见 `internal/risk`，装配层负责把它的错误翻成本包哨兵）。
//
// 三条边界：
//  1. **两个计数不许互相顶替**：`challenges.attempts` 是"这一次挑战试了几次"，
//     `login:fail:{user_id}` 是"这个账号错了几次"。前者用尽回 `ATTEMPTS_EXCEEDED`，
//     后者达阈值回 `ACCOUNT_LOCKED`。
//  2. **只认已绑定的用户**：匿名流（`flows.user_id IS NULL`）没有"账号"可锁，
//     如实不计数（不是"跳过保护"，是保护对象还不存在）。
//  3. **fail-closed**：计数不可用 → `ErrRiskUnavailable`（503），
//     **不得**降级成"这次先不算"。

// LockoutPolicy 是失败处理参数，来源是 `login_policies` 的 C 组两列。
type LockoutPolicy struct {
	// FailThreshold 连败多少次触发锁定（`fail_threshold`，> 0）。
	FailThreshold int
	// LockDuration 锁定时长，同时作为失败计数的窗口（`lock_duration_min`）。
	LockDuration time.Duration
}

// AccountGuard 是账号级失败计数的能力（实现见 `internal/risk`）。
type AccountGuard interface {
	// Status 读当前"还剩几次"与"是否已锁定"（用于下发挑战时的 `attempts_left` 与前置拦截）。
	Status(ctx context.Context, userID string, p LockoutPolicy) (attemptsLeft int, locked bool, err error)
	// RecordFailure 记一次失败；返回本次之后的剩余次数与是否已锁定。
	RecordFailure(ctx context.Context, userID string, p LockoutPolicy) (attemptsLeft int, locked bool, err error)
	// Reset 成功登录时清零（计数与锁定一起清）。
	Reset(ctx context.Context, userID string) error
}

// isCredentialFailure 判断这次失败是否该计入**账号级**失败数。
//
// 口径：只有"凭据不对"才计数（`INVALID_CREDENTIAL` / 动态码重放）。业务性拒绝——
// 例如"该步已完成"（`STEP_ALREADY_SATISFIED`）、"因子被禁用"——不该消耗用户的尝试额度：
// 那是用户点错了按钮，不是攻击信号。
func isCredentialFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, credential.ErrInvalidCredential) ||
		errors.Is(err, credential.ErrTemporalCodeReused)
}
