package store

import (
	"errors"
	"testing"
)

// D22 定稿口径（2026-11-11）的**纯计算**证据：未知类型只点名、不丢；坏值不当空集。
func TestUnknownFactorTypes(t *testing.T) {
	known := []string{"password", "totp"}
	cases := []struct {
		name     string
		disabled []string
		want     []string
	}{
		{"全部认识", []string{"password"}, nil},
		{"未知一个", []string{"password", "sms"}, []string{"sms"}},
		{"未知去重且排序", []string{"zzz", "sms", "zzz"}, []string{"sms", "zzz"}},
		{"空集", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnknownFactorTypes(tc.disabled, known)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// 读侧解码：`[]` 是唯一"空集"表达；`null` / 非数组 / 含空串都是坏值（不许静默变空集）。
func TestDecodeDisabledFactorsRejectsSilentEmpty(t *testing.T) {
	if got, err := decodeDisabledFactors(`[]`); err != nil || len(got) != 0 {
		t.Fatalf("[] 必须是合法空集：got=%v err=%v", got, err)
	}
	for _, bad := range []string{"", "null", `{}`, `"password"`, `[1]`, `[""]`} {
		if _, err := decodeDisabledFactors(bad); !errors.Is(err, ErrFactorDisabledStore) {
			t.Fatalf("%q 必须被当成坏值（ErrFactorDisabledStore），实际 %v", bad, err)
		}
	}
	got, err := decodeDisabledFactors(`["totp","password","totp"]`)
	if err != nil {
		t.Fatalf("正常值：%v", err)
	}
	if len(got) != 2 || got[0] != "password" || got[1] != "totp" {
		t.Fatalf("去重+排序后应为 [password totp]，实际 %v", got)
	}
}
