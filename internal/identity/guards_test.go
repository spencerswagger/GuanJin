package identity

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 装配守卫：缺 DB 时 `NewCredentialStore` 必须当场失败（不留半残对象）。
func TestNewCredentialStoreRequiresDB(t *testing.T) {
	if _, err := NewCredentialStore(nil); err == nil {
		t.Fatal("缺 DB 必须报错")
	}
}

// `registerMessage` 覆盖注册面会出现的每个码（前端拿这句话直接展示，缺一句就是空提示）。
func TestRegisterMessageCoversRegisterCodes(t *testing.T) {
	codes := []httpx.Code{
		httpx.CodeMissingParameter, httpx.CodeInvalidParam, httpx.CodeInvalidClient,
		httpx.CodeRegistrationNotAllowed, httpx.CodeInviteCodeInvalid,
		httpx.CodeIdentifierInvalidFormat, httpx.CodeIdentifierAlreadyBound,
		httpx.CodeChallengeExpired, httpx.CodeChallengeConsumed, httpx.CodeAttemptsExceeded,
		httpx.CodeWeakPassword, httpx.CodePasswordReused, httpx.CodePasswordPolicyViolation,
		httpx.CodeInvalidCredential,
	}
	for _, c := range codes {
		if msg := registerMessage(c); msg == "" || msg == "请求无法完成" {
			t.Fatalf("码 %s 缺少专属提示，实际 %q", c, msg)
		}
	}
	if msg := registerMessage(httpx.CodeInternalError); msg == "" {
		t.Fatal("未知码也要有兜底提示")
	}
}

// 请求体上限：超过 64KB → 400 `INVALID_REQUEST`（不是 500、不是"格式错"）。
func TestDecodeRegisterJSONTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	huge := `{"value":"` + strings.Repeat("a", maxRegisterBodyBytes+16) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register/start", strings.NewReader(huge))
	var dst startRequest
	if decodeRegisterJSON(rec, req, &dst) {
		t.Fatal("超限请求体必须被拒")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应为 400，实际 %d", rec.Code)
	}
}

// 令牌结构合法、MAC 正确，但 payload 不是 JSON → 仍判无效（不 panic、不猜测）。
func TestParseRegisterTokenRejectsBadPayloadJSON(t *testing.T) {
	key := newTokenKey(t)
	payload := base64.RawURLEncoding.EncodeToString([]byte("not-json"))
	mac := crypto.HMACSM3(key, []byte(registerTokenDomain+payload))
	token := "v1." + payload + "." + base64.RawURLEncoding.EncodeToString(mac)
	if _, err := parseRegisterToken(key, token); err == nil {
		t.Fatal("payload 非 JSON 必须判无效")
	}
}
