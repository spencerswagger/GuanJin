package flow

import (
	"errors"
	"testing"
)

func TestAddAMRIsMonotonicAndIdempotent(t *testing.T) {
	got, err := AddAMR(nil, "pwd")
	if err != nil || len(got) != 1 || got[0] != "pwd" {
		t.Fatalf("AddAMR(nil,pwd) = %v, %v", got, err)
	}
	got, err = AddAMR(got, "pwd", "totp") // 重复 pwd 应被去重
	if err != nil || len(got) != 2 || got[0] != "pwd" || got[1] != "totp" {
		t.Fatalf("AddAMR dedupe = %v, %v", got, err)
	}
	// 空因子一律拒绝（不许把空串写进 amr）
	if _, err := AddAMR(got, ""); err == nil {
		t.Fatalf("empty factor must be rejected")
	}
}

func TestCanTransition(t *testing.T) {
	if !CanTransition(StatusActive, StatusCompleted) || !CanTransition(StatusActive, StatusExpired) {
		t.Fatalf("active must be able to complete/expire")
	}
	for _, to := range []Status{StatusActive, StatusCompleted} {
		if CanTransition(StatusCompleted, to) {
			t.Errorf("completed is terminal, must not go to %s", to)
		}
	}
}

func TestAppendStepRejectsDuplicateFactor(t *testing.T) {
	steps := []CompletedStep{{Factor: "pwd", ChallengeID: "c1", At: "2026-11-02T10:00:00Z"}}
	_, err := AppendStep(steps, CompletedStep{Factor: "pwd", ChallengeID: "c2", At: "2026-11-02T10:01:00Z"})
	if !errors.Is(err, ErrStepAlreadySatisfied) {
		t.Fatalf("want ErrStepAlreadySatisfied (D3-F 409), got %v", err)
	}
	next, err := AppendStep(steps, CompletedStep{Factor: "totp", ChallengeID: "c3", At: "2026-11-02T10:02:00Z"})
	if err != nil || len(next) != 2 || next[0].ChallengeID != "c1" {
		t.Fatalf("append must not rewrite history: %v, %v", next, err)
	}
}
