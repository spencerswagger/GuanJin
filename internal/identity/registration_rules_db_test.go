//go:build integration

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 评审补口（P1-3 / P1-8 / P2-12）的真库用例：
//   - 该 client 禁用 `email_otp` 时，邮箱注册必须 `FACTOR_DISABLED`（422），且不落挑战行；
//   - `complete` 的凭据/策略失败要写 `auth_events(result='failure')`，而 `start` 的拒绝不写
//     （匿名可刷的入口写事件 = 给自己造审计洪水）；
//   - 弱口令在**消费挑战之前**就被拒（不白烧窗口）。

// setDisabledFactors 改该 client 的禁用因子（client_override 那一行）。
func (f *fixture) setDisabledFactors(t *testing.T, db *gorm.DB, factors string) {
	t.Helper()
	if err := db.Exec(`UPDATE login_policies SET disabled_factors = ?::text[] WHERE client_id = ?`,
		factors, f.clientID).Error; err != nil {
		t.Fatalf("update disabled_factors: %v", err)
	}
}

func TestAC_REG_StartRespectsDisabledFactors(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	f.setDisabledFactors(t, db, "{email_otp}")

	before := countRows(t, db, f.clientID)
	_, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4e-" + uuid.NewString()[:10] + "@example.com",
	})
	if !errors.Is(err, ErrFactorDisabled) {
		t.Fatalf("禁用了 email_otp 时邮箱注册应报 ErrFactorDisabled，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeFactorDisabled {
		t.Fatalf("应映射 FACTOR_DISABLED，实际 %q", code)
	}
	if after := countRows(t, db, f.clientID); after != before {
		t.Fatalf("禁用因子时不得落挑战行：before=%d after=%d", before, after)
	}

	// 手机号路径不受影响（禁用集是按因子的）。
	if _, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "phone", Value: "+8613700138000",
	}); err != nil {
		t.Fatalf("未被禁用的因子不该受影响：%v", err)
	}
	f.setDisabledFactors(t, db, "{}")
}

func TestCompleteWritesFailureEventButStartDoesNot(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	ctx := context.Background()

	countFailures := func() int64 {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM auth_events
			WHERE client_id = ? AND event_type = 'register' AND result = 'failure'`, f.clientID).Scan(&n).Error; err != nil {
			t.Fatalf("count auth_events: %v", err)
		}
		return n
	}
	_ = countFailures()

	// `start` 的拒绝（邀请码无效）**不**写失败事件。
	if _, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4e-" + uuid.NewString()[:10] + "@example.com",
		InviteCode: "NOPE",
	}); !errors.Is(err, ErrInviteCodeInvalid) {
		t.Fatalf("应报邀请码无效，实际 %v", err)
	}
	if n := countFailures(); n != 0 {
		t.Fatalf("start 的拒绝不该写失败事件，实际 %d 条", n)
	}

	// `complete` 的凭据失败要写，并且 `reason` 可定位。
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4e-" + uuid.NewString()[:12] + "@example.com",
		TraceID: "tr-fail-1",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: "000000", Password: "GuanJin#S4e-Strong1", TraceID: "tr-fail-2",
	}); !errors.Is(err, ErrInvalidRegisterCode) {
		t.Fatalf("错码应报 ErrInvalidRegisterCode，实际 %v", err)
	}
	if n := countFailures(); n != 1 {
		t.Fatalf("complete 的凭据失败应写 1 条失败事件，实际 %d", n)
	}
	var reason, traceID string
	if err := db.Raw(`SELECT coalesce(reason,''), coalesce(trace_id,'') FROM auth_events
		WHERE client_id = ? AND event_type = 'register' AND result = 'failure'
		ORDER BY occurred_at DESC LIMIT 1`, f.clientID).Row().Scan(&reason, &traceID); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if reason != "invalid_code" || traceID != "tr-fail-2" {
		t.Fatalf("失败事件应带 reason=invalid_code 与本次 trace_id，实际 %q / %q", reason, traceID)
	}
}

func TestCompletePrecheckRejectsWeakPasswordBeforeConsuming(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	ctx := context.Background()

	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4e-" + uuid.NewString()[:12] + "@example.com",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// 弱口令（太短）→ 在消费挑战**之前**就被拒。
	var rejected *PasswordRejected
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "short",
	}); !errors.As(err, &rejected) {
		t.Fatalf("弱口令应报 PasswordRejected，实际 %v", err)
	}
	// 挑战必须还没被消费：正确口令仍能建号。
	res, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4e-Strong1",
	})
	if err != nil {
		t.Fatalf("弱口令被拒后窗口仍应可用：%v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)
}
