package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件钉住 **AC-FC-02 在会话面那一半**：DB 不可用时会话端点必须 **503 `DB_UNAVAILABLE`**，
// 不降级放行、不伪装成 401（"库挂了"伪装成"你没登录"，运维会去查用户而不是查库），
// 响应里也不得出现任何身份字段。
//
// 不用 mock：错误来源是**真拨号一个没人监听的端口**（`127.0.0.1:1`）。

const deadDSN = "postgres://guanjin_test:x@127.0.0.1:1/guanjin_test?sslmode=disable"

// realConnectError 真拨号拿一个连接层错误。
func realConnectError(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, deadDSN)
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatal("127.0.0.1:1 居然连上了：本用例要求真拨号失败")
	}
	return err
}

// 码映射：仓储已判定的、原始连接层的，两类都必须是 `DB_UNAVAILABLE`；
// 业务错误与未知错误**不许**被算成依赖不可用。
func TestCodeOfDBUnavailable(t *testing.T) {
	mapped := &store.Error{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable}
	cases := []struct {
		name string
		err  error
		want httpx.Code
	}{
		{"仓储已判 DB_UNAVAILABLE", mapped, httpx.CodeDBUnavailable},
		{"原始连接层错误", realConnectError(t), httpx.CodeDBUnavailable},
		{"包裹后的连接层错误", wrapFailClosed(realConnectError(t)), httpx.CodeDBUnavailable},
		{"仓储哨兵仍走自己的码", store.ErrSessionNotFound, httpx.CodeSessionNotFound},
		{"业务错误不被依赖分支抢走", &store.Error{Code: httpx.CodeIdentifierAlreadyBound, Status: 409}, ""},
		{"未知错误没有码", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeOf(tc.err); got != tc.want {
				t.Fatalf("CodeOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// `GET /api/v1/me/sessions` 在库挂时必须 503 + `DB_UNAVAILABLE` + `Retry-After`，
// 且 `details` 为空（不带会话/身份列表）。
func TestListSessionsDBUnavailableIs503(t *testing.T) {
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	st := &failingListStore{fakeStore: newFakeStore(), err: realConnectError(t)}
	h := newSessionHTTP(t, st.fakeStore, &fakeFlows{}, &fakeEvents{}, nil, now)
	h.mgr = mustManagerWithStore(t, st, now)

	caller := httpx.Caller{Sub: uuid.New().String(), SID: uuid.New().String()}
	h.resolver = fakeResolver{sub: uuid.MustParse(caller.Sub), sid: uuid.MustParse(caller.SID)}

	req := withTrace(httptest.NewRequest(http.MethodGet, "/api/v1/me/sessions", nil))
	req.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	h.list(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（库挂必须 fail-closed）", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("503 必须带 Retry-After（D8-C）")
	}
	body := decodeError(t, rec)
	if body.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", body.Code, httpx.CodeDBUnavailable)
	}
	if body.TraceID == "" {
		t.Fatal("503 也必须给可复制 trace_id")
	}
	if body.Details != nil {
		t.Fatalf("依赖不可用时不得带 details（更不能带会话/身份），实际 %v", body.Details)
	}
	// 判据：期间**零个**"200 且带身份"的响应——这里连 2xx 都不允许出现。
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("库挂时不得出现 2xx，实际 %d", rec.Code)
	}
}

// `DELETE /api/v1/me/sessions/{id}` 在库挂时必须 503，且**不得**回 204（204 = 已经撤销成功）。
func TestRevokeSessionDBUnavailableIs503(t *testing.T) {
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	st := &failingListStore{fakeStore: newFakeStore(), err: realConnectError(t)}
	h := newSessionHTTP(t, st.fakeStore, &fakeFlows{}, &fakeEvents{}, nil, now)
	h.mgr = mustManagerWithStore(t, st, now)

	uid := uuid.New()
	h.resolver = fakeResolver{sub: uid, sid: uuid.New()}

	req := withTrace(httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+uuid.New().String(), nil))
	req.SetPathValue("id", uuid.New().String())
	req.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", body.Code, httpx.CodeDBUnavailable)
	}
	if rec.Code == http.StatusNoContent {
		t.Fatal("库挂时绝不能回 204（那等于宣称撤销成功）")
	}
}

// failingListStore 让**所有读路径**都失败：模拟"库挂了"时仓储出口的错误。
//
// 用嵌入而不是重写全部方法：真停库时每个仓储方法都会失败，这里只要保证
// "失败的错误形态与真库一致"（`MapError` 的产物 / 原始驱动错误）即可。
type failingListStore struct {
	*fakeStore
	err error
}

func (f *failingListStore) GetByID(context.Context, uuid.UUID) (*model.Session, error) {
	return nil, f.err
}

func (f *failingListStore) ListByUser(context.Context, uuid.UUID, int) ([]model.Session, error) {
	return nil, f.err
}

func (f *failingListStore) ActiveIDsByUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, f.err
}

func mustManagerWithStore(t *testing.T, st SessionStore, now time.Time) *Manager {
	t.Helper()
	m, err := NewManager(st, &fakeFlows{}, &fakeEvents{}, Options{Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func wrapFailClosed(err error) error { return &failClosedWrap{cause: err} }

type failClosedWrap struct{ cause error }

func (w *failClosedWrap) Error() string { return "wrap: " + w.cause.Error() }
func (w *failClosedWrap) Unwrap() error { return w.cause }

// 编译期确认：本文件没有引入未使用的依赖（json 供响应用例使用）。
var _ = json.Marshal
