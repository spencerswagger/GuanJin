package main

import (
	"testing"
	"time"
)

// 时长型环境变量只允许两种结果：**登记过的默认值**，或"显式给出且合法"。
// 解析失败必须报错（fail-closed），不许静默退回默认——否则"配错了"与"没配"在运行期无法区分。
func TestDurationEnv(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		def     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"未设置用默认值", "", 5 * time.Second, 5 * time.Second, false},
		{"显式值生效", "750ms", 5 * time.Second, 750 * time.Millisecond, false},
		{"非法时长报错", "5 seconds", time.Second, 0, true},
		{"零值报错（不允许关掉超时）", "0s", time.Second, 0, true},
		{"负值报错", "-1s", time.Second, 0, true},
		{"默认值为 0 且未设置（时钟偏移的合法默认）", "", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GUANJIN_TEST_DURATION", tc.value)
			got, err := durationEnv("GUANJIN_TEST_DURATION", tc.def)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("期望 %s，实际 %s", tc.want, got)
			}
		})
	}
}
