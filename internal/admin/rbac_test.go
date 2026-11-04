package admin

import (
	"errors"
	"testing"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
)

// 三员矩阵的单测：AC-AUD-07 的两条硬语义在这里逐条钉住——
// 「审计员写操作零成功」（→ THREE_POWER_VIOLATION）与「单纯缺角色」（→ INSUFFICIENT_ROLE）。

func caller(roles ...string) *session.Caller {
	return &session.Caller{Roles: roles}
}

func TestAuthorizeSessionsMatrix(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		write bool
		want  error
	}{
		{"sys admin read", []string{"system_admin"}, false, nil},
		{"sys admin write", []string{"system_admin"}, true, nil},
		{"sec admin read", []string{"security_admin"}, false, nil},
		{"sec admin write", []string{"security_admin"}, true, nil},
		{"auditor read", []string{"auditor"}, false, nil},
		{"auditor write", []string{"auditor"}, true, ErrThreePowerViolation},
		{"auditor+sys write", []string{"auditor", "system_admin"}, true, nil},
		{"no role read", nil, false, ErrInsufficientRole},
		{"no role write", nil, true, ErrInsufficientRole},
		{"business role only read", []string{"order_admin"}, false, ErrInsufficientRole},
		{"business role only write", []string{"order_admin"}, true, ErrInsufficientRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuthorizeSessions(caller(tc.roles...), tc.write)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestAuthorizeSessionsNilCaller(t *testing.T) {
	if err := AuthorizeSessions(nil, false); !errors.Is(err, ErrInsufficientRole) {
		t.Fatalf("want ErrInsufficientRole, got %v", err)
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{ErrInsufficientRole, httpx.CodeInsufficientRole},
		{ErrThreePowerViolation, httpx.CodeThreePowerViolation},
		{errors.New("other"), ""},
	}
	for i, tc := range cases {
		if got := CodeOf(tc.err); got != tc.want {
			t.Fatalf("case %d: got %q, want %q", i, got, tc.want)
		}
	}
}

func TestRoleConstantsAreDistinct(t *testing.T) {
	roles := []Role{RoleSystemAdmin, RoleSecurityAdmin, RoleAuditor}
	seen := map[Role]bool{}
	for _, r := range roles {
		if r == "" {
			t.Fatal("role constant must not be empty")
		}
		if seen[r] {
			t.Fatalf("duplicate role %q", r)
		}
		seen[r] = true
	}
}

func TestAuditorWriteCannotBeEnabled(t *testing.T) {
	// 审计员写权限是**硬约束**（接口 §3.8 引言 / AC-AUD-07），不允许被配置打开。
	if allowAuditorWrite {
		t.Fatal("allowAuditorWrite must stay false")
	}
}
