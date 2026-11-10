//go:build integration

package session

import (
	"context"
	"errors"
	"testing"

	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// AC-SESS-03（S11 收口补证）：**撤销后新登录产生新 `session_id`，旧 `session_id` 的令牌永久失效**。
//
// 为什么单开用例：`TestE2E_SessionRevokeMakesTokenUseless`（cmd/guanjind）证的是"撤销后
// 令牌立刻不可用"，但判据还有两件它没断言的事 ——
//
//	① **重新登录必须换 id**（不能"复用"旧行把用户放回去）；
//	② **旧 id 的失效是永久的**（新会话活跃了、过期清理跑过了，旧 id 仍必须被拒）。
//
// 断言落点选 `Manager.Validate`：它就是令牌校验链路（S6 的 `protocol.CallerResolver`）
// 最终要过的那道门；在这里为真，才谈得上"令牌永久失效"。
func TestAC_SESS_03_ReloginGetsNewSessionIDAndOldStaysDead(t *testing.T) {
	db := openSessionDB(t)
	f := newSessionFixture(t, db)
	ctx := context.Background()

	m, err := NewManager(store.NewSessionOpsRepo(db), store.NewFlowRepo(db),
		store.NewAuthEventRepo(db), Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// ① 第一次登录：会话可用。
	oldID := f.insertActiveSession(t, db)
	if _, err := m.Validate(ctx, oldID); err != nil {
		t.Fatalf("新会话应可用：%v", err)
	}

	// ② 用户自助登出（撤销）。
	if err := m.Revoke(ctx, RevokeInput{
		SessionID: oldID, Reason: ReasonLogout, EventType: EventLogout,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// ③ 撤销即失效（不是"下一次请求再算"）。
	if _, err := m.Validate(ctx, oldID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("撤销后旧会话应 ErrSessionRevoked，实际 %v", err)
	}

	// ④ 重新登录：必须拿到**新的** session_id，且新会话可用。
	newID := f.insertActiveSession(t, db)
	if newID == oldID {
		t.Fatal("重新登录不得复用旧 session_id（主键复用等于把被撤销的会话放回来）")
	}
	if _, err := m.Validate(ctx, newID); err != nil {
		t.Fatalf("新会话应可用：%v", err)
	}

	// ⑤ 旧 id 的失效是**永久**的：新会话活跃、清理任务跑过之后仍然拒。
	if _, err := m.ExpireStale(ctx); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if _, err := m.Validate(ctx, oldID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("新会话存在时旧会话仍必须 ErrSessionRevoked，实际 %v", err)
	}

	// ⑥ 库里的事实：旧行只有一条、状态仍是 revoked（没被清理/重建回 active）。
	var rows int64
	if err := db.Model(&model.Session{}).Where("id = ?", oldID).Count(&rows).Error; err != nil {
		t.Fatalf("数旧会话行：%v", err)
	}
	if rows != 1 {
		t.Fatalf("旧 session_id 应恰好一行（不得被重建/复制），实际 %d", rows)
	}
	var old model.Session
	if err := db.Where("id = ?", oldID).Take(&old).Error; err != nil {
		t.Fatalf("回读旧会话：%v", err)
	}
	if old.Status != model.SessionStatusRevoked {
		t.Fatalf("旧会话 status 应仍为 revoked，实际 %q", old.Status)
	}
	if old.RevokedAt == nil {
		t.Fatal("旧会话必须有 revoked_at（可举证「何时失效」）")
	}

	// ⑦ 两个会话同属一人：新会话的存在不得让旧会话"复活"（判据的「永久」二字）。
	var active int64
	if err := db.Model(&model.Session{}).
		Where("user_id = ? AND status = ?", f.userID, model.SessionStatusActive).
		Count(&active).Error; err != nil {
		t.Fatalf("数活跃会话：%v", err)
	}
	if active != 1 {
		t.Fatalf("应为 1 条活跃会话（新的那条），实际 %d", active)
	}
}
