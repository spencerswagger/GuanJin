package store

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

func pgErr(code, constraint string) error {
	return &pgconn.PgError{Code: code, ConstraintName: constraint, TableName: "t"}
}

// 双键映射：同一个 SQLSTATE 下，不同约束名必须给出不同业务码/状态。
func TestMapErrorDoubleKey(t *testing.T) {
	cases := []struct {
		name       string
		code       string
		constraint string
		wantCode   httpx.Code
		wantStatus int
	}{
		{"identifier unique", sqlStateUniqueViolation, "identifiers_type_value_uniq", codeIdentifierAlreadyBound, 409},
		{"username unique", sqlStateUniqueViolation, "users_username_lower_uidx", codeIdentifierAlreadyBound, 409},
		{"role binding unique", sqlStateUniqueViolation, "role_bindings_uniq", codeBindingAlreadyExists, 409},
		{"policy snapshot unique", sqlStateUniqueViolation, "policy_snapshots_uniq", codePolicySnapshotConflict, 409},
		{"flow active unique", sqlStateUniqueViolation, "flows_one_active_per_user_client_uniq", codeCooldownActive, 409},
		{"clients pkey", sqlStateUniqueViolation, "clients_pkey", codeInvalidClient, 400},
		{"kid unique", sqlStateUniqueViolation, "signing_keys_kid_uniq", codeInvalidParameter, 400},

		// 23514：默认 400，但方向不变式必须 422 —— 这就是"禁止 23514 一律 400"的证据。
		{"direction invariant", sqlStateCheckViolation, "login_policies_refresh_le_absolute", codeTierDirectionViolation, 422},
		{"acr domain", sqlStateCheckViolation, "sessions_acr_check", codeInvalidParameter, 400},
		{"flow completed needs user", sqlStateCheckViolation, "flows_completed_requires_user", codeInvalidParameter, 400},
		{"unknown check", sqlStateCheckViolation, "some_unregistered_check", codeInvalidParameter, 400},

		// 23503：外键违反 = 被引用对象不存在 → *_NOT_FOUND（404）。
		{"flow user fk", sqlStateForeignKeyViolation, "flows_user_id_fkey", codeUserNotFound, 404},
		{"flow client fk", sqlStateForeignKeyViolation, "flows_client_id_fkey", codeClientNotFound, 404},
		{"unregistered fk", sqlStateForeignKeyViolation, "something_fkey", codeInvalidParameter, 400},

		{"not null", sqlStateNotNullViolation, "", codeMissingParameter, 400},
		{"bad uuid text", sqlStateInvalidTextRepr, "", codeInvalidParameter, 400},
		{"numeric range", sqlStateNumericOutOfRange, "", codeInvalidParameter, 400},
		{"db gone", sqlStateConnectionFailure, "", codeDBUnavailable, 503},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := MapError(pgErr(tc.code, tc.constraint))
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("MapError returned %T, want *store.Error", err)
			}
			if e.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", e.Code, tc.wantCode)
			}
			if e.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", e.Status, tc.wantStatus)
			}
			if e.SQLState != tc.code {
				t.Errorf("sqlstate = %q, want %q", e.SQLState, tc.code)
			}
			if !httpx.IsKnownCode(e.Code) {
				t.Errorf("code %q is not in the frozen §5.4 registry", e.Code)
			}
			if e.Status != httpx.HTTPStatus(e.Code) {
				t.Errorf("status %d disagrees with httpx.HTTPStatus(%s)=%d",
					e.Status, e.Code, httpx.HTTPStatus(e.Code))
			}
		})
	}
}

// 未登记的唯一约束 / CHECK / 只增表触发器违反 **不得**被硬映射成业务码：
// 硬映射会把"代码缺陷"伪装成"用户错误"，从而永远没人去修。
func TestUnmappedErrorsStayDefects(t *testing.T) {
	for _, err := range []error{
		pgErr(sqlStateUniqueViolation, "identifiers_one_primary_per_user"),
		pgErr(sqlStateUniqueViolation, "user_visited_clients_uniq"),
		pgErr(sqlStateUniqueViolation, "totally_new_constraint"),
		pgErr(sqlStateUniqueViolation, "users_pkey"),
		pgErr(sqlStateRestrictViolation, ""),
		pgErr(sqlStateUndefinedTable, ""),
		pgErr(sqlStateUndefinedColumn, ""),
	} {
		mapped := MapError(err)
		var e *Error
		if !errors.As(mapped, &e) {
			t.Fatalf("MapError(%v) returned %T", err, mapped)
		}
		if e.Mapped() {
			t.Errorf("%s/%s must stay unmapped (defect), got code %q",
				e.SQLState, e.Constraint, e.Code)
		}
		if ConstraintOf(mapped) == "" && e.SQLState != sqlStateUndefinedTable &&
			e.SQLState != sqlStateUndefinedColumn && e.SQLState != sqlStateRestrictViolation {
			t.Errorf("unmapped error must keep the constraint name for diagnosis: %v", mapped)
		}
	}
}

func TestMapErrorPassThroughAndNil(t *testing.T) {
	if err := MapError(nil); err != nil {
		t.Fatalf("MapError(nil) = %v, want nil", err)
	}
	plain := errors.New("boom")
	if got := MapError(plain); !errors.Is(got, plain) {
		t.Fatalf("non-pg error must pass through, got %v", got)
	}
	// 被包装的 PG 错误也要认出来（仓储/驱动链路上常有多层 wrap）。
	wrapped := fmt.Errorf("context: %w", pgErr(sqlStateUniqueViolation, "role_bindings_uniq"))
	var e *Error
	if !errors.As(MapError(wrapped), &e) || e.Code != codeBindingAlreadyExists {
		t.Fatalf("wrapped pg error must be mapped, got %v", MapError(wrapped))
	}
}

func TestRetryableAndIsNotFound(t *testing.T) {
	for _, state := range []string{sqlStateSerializationFail, sqlStateDeadlockDetected} {
		if !Retryable(MapError(pgErr(state, ""))) {
			t.Errorf("sqlstate %s must be retryable", state)
		}
	}
	for _, state := range []string{sqlStateUniqueViolation, sqlStateCheckViolation, sqlStateNotNullViolation} {
		if Retryable(MapError(pgErr(state, ""))) {
			t.Errorf("sqlstate %s must not be retryable", state)
		}
	}
	if !IsNotFound(MapError(pgErr(sqlStateForeignKeyViolation, "flows_user_id_fkey"))) {
		t.Error("fk violation must classify as not-found")
	}
	if IsNotFound(MapError(pgErr(sqlStateUniqueViolation, "role_bindings_uniq"))) {
		t.Error("unique violation must not classify as not-found")
	}
	if Retryable(nil) || IsNotFound(nil) || ConstraintOf(nil) != "" {
		t.Error("nil handling must be safe")
	}
}

// Error() 的文本里不得出现 SQL 与参数值（防敏感数据进日志）。
func TestErrorTextHasNoSQLOrValues(t *testing.T) {
	err := MapError(&pgconn.PgError{
		Code: sqlStateUniqueViolation, ConstraintName: "identifiers_type_value_uniq",
		TableName: "identifiers", Detail: "Key (type, value)=(email, \x2a\x2a\x2a) already exists.",
	})
	msg := err.Error()
	for _, forbidden := range []string{"INSERT", "SELECT", "UPDATE", "DELETE", "Key ("} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("error text must not carry SQL/detail, got %q", msg)
		}
	}
	if !strings.Contains(msg, "identifiers_type_value_uniq") {
		t.Fatalf("error text must carry the constraint name for diagnosis, got %q", msg)
	}
}

// 每条业务码都必须落在 httpx 的冻结码表里（防止数据层悄悄造码，D3-B）。
func TestAllStoreCodesAreFrozen(t *testing.T) {
	for _, c := range []httpx.Code{
		codeIdentifierAlreadyBound, codeBindingAlreadyExists, codePolicySnapshotConflict,
		codeCooldownActive, codeTierDirectionViolation, codeUserNotFound, codeClientNotFound,
		codeIdentifierNotFound, codeSessionNotFound, codeFlowNotFound, codeInvalidParameter,
		codeMissingParameter, codeInvalidClient, codeInvalidRequest, codeDBUnavailable,
	} {
		if !httpx.IsKnownCode(c) {
			t.Errorf("store uses code %q which is not in the frozen registry", c)
		}
		if httpx.HTTPStatus(c) == 0 {
			t.Errorf("code %q has no HTTP status", c)
		}
	}
}

// 每个登记的码在 §5.4 里的状态必须与我们对外的期望一致：
// 409 组、422 组、404 组各抽查一条，避免"码对了、状态错了"。
func TestStoreStatusExpectations(t *testing.T) {
	want := map[httpx.Code]int{
		codeIdentifierAlreadyBound: http.StatusConflict,
		codeTierDirectionViolation: http.StatusUnprocessableEntity,
		codeUserNotFound:           http.StatusNotFound,
		codeInvalidParameter:       http.StatusBadRequest,
		codeDBUnavailable:          http.StatusServiceUnavailable,
	}
	for code, status := range want {
		if got := httpx.HTTPStatus(code); got != status {
			t.Errorf("HTTPStatus(%s) = %d, want %d", code, got, status)
		}
	}
}

// 映射表里的 reason 必须落到 Error 上：排障日志要能直接说明"为什么是 409"。
func TestMappedErrorCarriesReason(t *testing.T) {
	err := MapError(pgErr(sqlStateUniqueViolation, "flows_one_active_per_user_client_uniq"))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *store.Error, got %T", err)
	}
	if e.Reason == "" {
		t.Fatal("mapped error must carry the mapping reason")
	}
	if strings.Contains(e.Error(), e.Reason) {
		t.Fatal("Error() text must stay short; reason is for logs, not for the wire")
	}
}
