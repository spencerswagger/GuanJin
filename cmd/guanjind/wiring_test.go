package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
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

// 缺依赖 → 装配期就失败（不等到第一个请求）。
func TestNewFlowHandlerRequiresDependencies(t *testing.T) {
	if _, err := newFlowHandler(context.Background(), nil, time.Minute, discardLogger()); err == nil {
		t.Fatal("缺 DB 必须装配失败")
	}
}
