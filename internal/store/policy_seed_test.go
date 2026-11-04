package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// D13 的启动期校验必须能在**没有数据库**时被钉住：
// 规则（每档恰一行、越界档位算漂移）是纯函数，用表驱动直接打；
// 查询形态用 DryRun 断言（"查不到行 = 报缺失，而不是判绿"这条最容易写反）。

func TestValidateTierDefaultCounts(t *testing.T) {
	cases := []struct {
		name       string
		counts     map[string]int
		wantErr    bool
		wantMiss   []string
		wantDup    []string
		wantUnwant []string
	}{
		{
			name:   "三档各恰一行 → 通过",
			counts: map[string]int{"L1": 1, "L2": 1, "L3": 1},
		},
		{
			name:     "缺 L3 → 失败并点名 L3",
			counts:   map[string]int{"L1": 1, "L2": 1},
			wantErr:  true,
			wantMiss: []string{"L3"},
		},
		{
			name:     "三档全缺（空表）→ 失败并点满三档",
			counts:   map[string]int{},
			wantErr:  true,
			wantMiss: []string{"L1", "L2", "L3"},
		},
		{
			name:    "nil map 与空表同义（不许 panic、不许判绿）",
			counts:  nil,
			wantErr: true,
			// nil map 读出来也是 0，Missing 依然是三档。
			wantMiss: []string{"L1", "L2", "L3"},
		},
		{
			name:    "L2 多一行 → 失败并点名 L2（唯一索引本应拦住，出现即 schema 漂移）",
			counts:  map[string]int{"L1": 1, "L2": 2, "L3": 1},
			wantErr: true,
			wantDup: []string{"L2"},
		},
		{
			name:       "出现第四档 → 失败（D12：V1 没有 AAL3、档位也只有三档）",
			counts:     map[string]int{"L1": 1, "L2": 1, "L3": 1, "L4": 1},
			wantErr:    true,
			wantUnwant: []string{"L4"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTierDefaultCounts(tc.counts)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("期望通过，实际 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("期望失败，实际 nil")
			}
			if !errors.Is(err, ErrTierDefaultSeed) {
				t.Fatalf("err = %v, want errors.Is(ErrTierDefaultSeed)", err)
			}
			var seedErr *TierDefaultSeedError
			if !errors.As(err, &seedErr) {
				t.Fatalf("err = %T, want *TierDefaultSeedError", err)
			}
			if got := strings.Join(seedErr.Missing, ","); got != strings.Join(tc.wantMiss, ",") {
				t.Errorf("Missing = %q, want %q", got, strings.Join(tc.wantMiss, ","))
			}
			if got := strings.Join(seedErr.Duplicated, ","); got != strings.Join(tc.wantDup, ",") {
				t.Errorf("Duplicated = %q, want %q", got, strings.Join(tc.wantDup, ","))
			}
			if got := strings.Join(seedErr.Unexpected, ","); got != strings.Join(tc.wantUnwant, ",") {
				t.Errorf("Unexpected = %q, want %q", got, strings.Join(tc.wantUnwant, ","))
			}
		})
	}
}

// 错误文案要能直接对账：结论 + 逐档计数 + 点名异常档位（排障时进程已经退了，不该再连库）。
func TestTierDefaultSeedErrorMessage(t *testing.T) {
	err := ValidateTierDefaultCounts(map[string]int{"L1": 1, "L2": 2})
	if err == nil {
		t.Fatal("期望失败，实际 nil")
	}
	msg := err.Error()
	for _, want := range []string{"各恰一行", "L1=1", "L2=2", "L3=0", "缺失 L3", "多行 L2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误文案缺少 %q：%s", want, msg)
		}
	}
}

// 查询形态用 DryRun 钉住；"空表 / 缺一档 → 失败"的**真实**判定放在 integration 用例
// （`policy_seed_db_test.go`）——DryRun 不支持执行读操作（`gorm.ErrDryRunModeUnsupported`），
// 在这里假装验"空结果"只会得到一个假绿。
//
// 这里额外钉一条 fail-closed：**查询本身失败时不得吞掉错误**（吞了就等于"没查=通过"）。
func TestVerifyTierDefaultsSQLShape(t *testing.T) {
	ctx := context.Background()

	if err := VerifyTierDefaults(ctx, nil); err == nil {
		t.Fatal("nil db 必须报错（不能静默跳过启动校验）")
	}

	db, capture := dryRunDB(t)
	if err := VerifyTierDefaults(ctx, db); err == nil {
		t.Fatal("查询失败必须返回错误（不许把'没查到'当'校验通过'）")
	}

	sql := capture.last(t)
	for _, want := range []string{
		"FROM login_policies",
		"scope = 'tier_default'",
		"tier IS NOT NULL",
		"GROUP BY tier",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL 缺少 %q：%s", want, sql)
		}
	}
}
