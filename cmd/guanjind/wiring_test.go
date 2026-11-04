package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/risk"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 库里的禁用因子名必须能折成合法类型；认不出就报错（不静默忽略——否则"禁了"其实没禁）。
func TestParseFactorTypes(t *testing.T) {
	got, err := parseFactorTypes([]string{"totp", "sms_otp"})
	if err != nil {
		t.Fatalf("合法因子名不该失败：%v", err)
	}
	if len(got) != 2 || got[0] != factor.FactorTOTP || got[1] != factor.FactorSMSOTP {
		t.Fatalf("折算结果不符：%v", got)
	}
	if got, err := parseFactorTypes(nil); err != nil || len(got) != 0 {
		t.Fatalf("空集合应是空切片而不是错误：%v %v", got, err)
	}
	for _, bad := range []string{"TOTP", "sms otp", ""} {
		if _, err := parseFactorTypes([]string{bad}); err == nil {
			t.Fatalf("非法因子名 %q 必须报错", bad)
		}
	}
}

// 业务系统不存在/停用要落到 `INVALID_CLIENT`（400），不能变成 500。
func TestClientErrorMapsToInvalidClient(t *testing.T) {
	for _, src := range []error{store.ErrClientNotFound, store.ErrClientDisabled} {
		if err := clientError(src); !errors.Is(err, flow.ErrInvalidClient) {
			t.Fatalf("%v 应映射 ErrInvalidClient，实际 %v", src, err)
		}
	}
	other := errors.New("db down")
	if err := clientError(other); !errors.Is(err, other) {
		t.Fatalf("未知错误必须原样上抛，实际 %v", err)
	}
}

// 风控错误 → 编排哨兵：不翻的话 `ACCOUNT_LOCKED` 与 `REDIS_UNAVAILABLE` 都会变成 500。
func TestMapRiskError(t *testing.T) {
	if err := mapRiskError(nil); err != nil {
		t.Fatalf("nil 应保持 nil，实际 %v", err)
	}
	if err := mapRiskError(risk.ErrLocked); !errors.Is(err, flow.ErrAccountLocked) {
		t.Fatalf("锁定应映射 ErrAccountLocked，实际 %v", err)
	}
	if err := mapRiskError(risk.ErrUnavailable); !errors.Is(err, flow.ErrRiskUnavailable) {
		t.Fatalf("不可用应映射 ErrRiskUnavailable，实际 %v", err)
	}
	other := errors.New("policy broken")
	if err := mapRiskError(other); !errors.Is(err, other) {
		t.Fatalf("未知错误必须原样上抛，实际 %v", err)
	}
}

// 策略换算：分钟 → Duration，字段一一对应（不在两侧各写一套默认值）。
func TestToRiskPolicy(t *testing.T) {
	got := toRiskPolicy(flow.LockoutPolicy{FailThreshold: 5, LockDuration: 15 * time.Minute})
	if got.FailThreshold != 5 || got.LockDuration != 15*time.Minute {
		t.Fatalf("换算不符：%+v", got)
	}
}

// 缺依赖 → 装配期就失败（不等到第一个请求）。
func TestNewFlowHandlerRequiresDependencies(t *testing.T) {
	if _, _, err := newFlowHandler(context.Background(), nil, nil, store.StartupConfig{}, discardLogger()); err == nil {
		t.Fatal("缺 DB 必须装配失败")
	}
}
