package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是**注册面**的 HTTP 入口（端点名照抄接口清单 §2.3 的冻结名）：
//
//	POST /auth/register/start
//	POST /auth/register/complete
//
// 处理器只做三件事：解码 → 调服务 → 按码表写出。**没有任何业务判断**：
// 准入、邀请码、唯一性、口令策略、挑战状态机全部在服务层（`registration.go` / `service.go`），
// 否则同一条规则会在两处各写一遍，然后慢慢不一致。
//
// 未覆盖（属 S6 协议面）：`complete` 的响应只有 `ok=true`。三件令牌
// （`access_token`/`id_token`/`refresh_token`）由令牌签发端点负责，本切片不签发、也不编造字段。

const maxRegisterBodyBytes = 64 << 10

// Registrar 是处理器需要的最小能力面（实现是 `*Service`）。
//
// 用接口而不是直接吃 `*Service`：处理器没有任何业务判断，它只该依赖"两个方法"；
// 这样 handler 的解码/码表/响应形状可以在**不建库**的前提下被测到。
type Registrar interface {
	StartRegistration(ctx context.Context, req StartRegistrationRequest) (*StartRegistrationResult, error)
	CompleteRegistration(ctx context.Context, req CompleteRegistrationRequest) (*RegisterResult, error)
}

// RegisterHTTP 是注册面两个端点的处理器。
type RegisterHTTP struct {
	svc    Registrar
	logger *slog.Logger
}

// NewRegisterHTTP 构造处理器（缺依赖即装配期失败）。
func NewRegisterHTTP(svc Registrar, logger *slog.Logger) (*RegisterHTTP, error) {
	if svc == nil {
		return nil, ErrNilProvider
	}
	if logger == nil {
		return nil, ErrNilProvider
	}
	return &RegisterHTTP{svc: svc, logger: logger}, nil
}

// Routes 挂上两个冻结路径。
func (h *RegisterHTTP) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register/start", h.start)
	mux.HandleFunc("POST /auth/register/complete", h.complete)
}

type startRequest struct {
	ClientID   string `json:"client_id"`
	Type       string `json:"type"`
	Value      string `json:"value"`
	InviteCode string `json:"invite_code"`
}

type startResponse struct {
	RegisterToken string  `json:"register_token"`
	ChallengeID   *string `json:"challenge_id,omitempty"`
	NextAction    string  `json:"next_action"`
	TargetMasked  string  `json:"target_masked,omitempty"`
	AttemptsLeft  int     `json:"attempts_left,omitempty"`
	ExpiresAt     string  `json:"expires_at"`
}

type completeRequest struct {
	RegisterToken string `json:"register_token"`
	Code          string `json:"code"`
	Password      string `json:"password"`
}

type completeResponse struct {
	OK bool `json:"ok"`
}

func (h *RegisterHTTP) start(w http.ResponseWriter, r *http.Request) {
	var body startRequest
	if !decodeRegisterJSON(w, r, &body) {
		return
	}
	res, err := h.svc.StartRegistration(r.Context(), StartRegistrationRequest{
		ClientID:   body.ClientID,
		Type:       body.Type,
		Value:      body.Value,
		InviteCode: body.InviteCode,
		TraceID:    httpx.TraceIDFromContext(r.Context()),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeRegisterJSON(w, h.logger, http.StatusOK, startResponse{
		RegisterToken: res.RegisterToken,
		ChallengeID:   res.ChallengeID,
		NextAction:    string(res.NextAction),
		TargetMasked:  res.TargetMasked,
		AttemptsLeft:  res.AttemptsLeft,
		ExpiresAt:     res.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (h *RegisterHTTP) complete(w http.ResponseWriter, r *http.Request) {
	var body completeRequest
	if !decodeRegisterJSON(w, r, &body) {
		return
	}
	if _, err := h.svc.CompleteRegistration(r.Context(), CompleteRegistrationRequest{
		RegisterToken: body.RegisterToken,
		Code:          body.Code,
		Password:      body.Password,
		TraceID:       httpx.TraceIDFromContext(r.Context()),
	}); err != nil {
		h.fail(w, r, err)
		return
	}
	writeRegisterJSON(w, h.logger, http.StatusOK, completeResponse{OK: true})
}

// fail 把服务层错误写成统一四字段响应（`trace_id` 可复制，文案不承载细节）。
func (h *RegisterHTTP) fail(w http.ResponseWriter, r *http.Request, err error) {
	traceID := httpx.TraceIDFromContext(r.Context())
	code, ok := CodeOf(err)
	if !ok {
		// 不认识的错误：详细原因只进日志，按 trace_id 可查；对外统一 500。
		h.logger.ErrorContext(r.Context(), "identity: unhandled register error",
			"trace_id", traceID, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, traceID, httpx.CodeInternalError, "服务内部错误，请稍后用同一 trace_id 重试", nil)
		return
	}
	httpx.WriteError(w, traceID, code, registerMessage(code), nil)
}

// registerMessage 给码配一句面向用户的提示。
func registerMessage(c httpx.Code) string {
	switch c {
	case httpx.CodeMissingParameter:
		return "缺少必要的参数"
	case httpx.CodeInvalidParam:
		return "参数不合法"
	case httpx.CodeInvalidClient:
		return "业务系统不存在或已停用"
	case httpx.CodeRegistrationNotAllowed:
		return "该业务系统当前不开放自助注册"
	case httpx.CodeInviteCodeInvalid:
		return "邀请码无效"
	case httpx.CodeIdentifierInvalidFormat:
		return "邮箱/手机号/用户名格式不正确"
	case httpx.CodeIdentifierAlreadyBound:
		return "该账号已被注册"
	case httpx.CodeChallengeExpired:
		return "注册窗口已超时，请重新开始"
	case httpx.CodeChallengeConsumed:
		return "注册窗口已使用，请重新开始"
	case httpx.CodeAttemptsExceeded:
		return "尝试次数过多，请重新开始注册"
	case httpx.CodeWeakPassword:
		return "口令强度不足"
	case httpx.CodePasswordReused:
		return "口令重复使用"
	case httpx.CodePasswordPolicyViolation:
		return "口令不符合安全要求"
	case httpx.CodeInvalidCredential:
		return "验证码或口令不正确"
	default:
		return "请求无法完成"
	}
}

func decodeRegisterJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeInvalidRequest, "请求体过大", nil)
			return false
		}
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeInvalidRequest, "请求体不是合法 JSON", nil)
		return false
	}
	return true
}

func writeRegisterJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("identity: write response failed", "error", err)
	}
}
