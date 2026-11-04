package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是 **因子与挑战面的 HTTP 入口**（S5，端点名照抄接口清单 §2.2 / §3.1.4 的冻结名）：
//
//	POST /auth/challenge/{factor}   下发一次挑战
//	POST /auth/verify/{factor}      校验挑战应答并推进流
//	GET  /authorize/flow            取 Flow Descriptor
//
// 为什么放在 `internal/flow` 而不是另开一层：这三个端点的**全部**语义都是编排语义
// （`ACR_INSUFFICIENT` 要带 `details.flow`、`STEP_ALREADY_SATISFIED` 的码只能在本包出现、
// 不可达是 200 而不是错误）。把 handler 搬远，就得把这三条语义再抄一遍。
// 处理器本身保持"薄"：只解码、调编排、把错误按码表写出，**不做任何业务判断**。
//
// 未知错误（含 DB 不可用）在这里一律 500：E6 的 503 细分是 S10 的接线范围
// （`/readyz` + D8-C 映射），本切片不自行猜"是不是依赖挂了"。

const maxRequestBytes = 64 << 10

// HTTPHandler 是这三个端点的处理器（无本地状态，多副本行为一致）。
type HTTPHandler struct {
	orch   *Orchestrator
	logger *slog.Logger
}

// NewHTTPHandler 构造处理器；缺依赖即报错（装配期 fail-fast）。
func NewHTTPHandler(orch *Orchestrator, logger *slog.Logger) (*HTTPHandler, error) {
	switch {
	case orch == nil:
		return nil, fmt.Errorf("%w: orchestrator", ErrNilDependency)
	case logger == nil:
		return nil, fmt.Errorf("%w: logger", ErrNilDependency)
	}
	return &HTTPHandler{orch: orch, logger: logger}, nil
}

// Orchestrator 暴露编排器本身（协议面要复用同一条编排链路，避免两套 Descriptor 口径）。
func (h *HTTPHandler) Orchestrator() *Orchestrator { return h.orch }

// Routes 把三个冻结路径挂到 mux 上（方法 + 通配由 `net/http` 的 pattern 表达）。
func (h *HTTPHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/challenge/{factor}", h.challenge)
	mux.HandleFunc("POST /auth/verify/{factor}", h.verify)
	mux.HandleFunc("GET /authorize/flow", h.describe)
}

// challengeRequest 是 `POST /auth/challenge/{factor}` 的请求体（接口 §3.2.1）。
//
// 字段名即契约；`flow_id` / `client_id` 用指针是为了区分**缺省**与"空串"——前者对
// `login`/`step_up` 是硬错（CH-1），后者由编排层自己判，不在这里替它做决定。
type challengeRequest struct {
	FlowID   *string `json:"flow_id"`
	Purpose  string  `json:"purpose"`
	ClientID string  `json:"client_id"`
}

type challengeResponse struct {
	FlowID       *string           `json:"flow_id,omitempty"`
	ChallengeID  *string           `json:"challenge_id,omitempty"`
	NextAction   factor.NextAction `json:"next_action"`
	TargetMasked string            `json:"target_masked,omitempty"`
	TTLSeconds   int64             `json:"ttl,omitempty"`
	AttemptsLeft int               `json:"attempts_left,omitempty"`
	ExpiresAt    *time.Time        `json:"expires_at,omitempty"`
}

type verifyRequest struct {
	FlowID      string `json:"flow_id"`
	ChallengeID string `json:"challenge_id"`
	Response    string `json:"response"`
}

type verifyResponse struct {
	Verified bool        `json:"verified"`
	AMR      []string    `json:"amr"`
	ACR      factor.ACR  `json:"acr"`
	Flow     *Descriptor `json:"flow,omitempty"`
}

func (h *HTTPHandler) challenge(w http.ResponseWriter, r *http.Request) {
	var body challengeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	var flowID uuid.UUID
	if body.FlowID != nil && *body.FlowID != "" {
		parsed, err := uuid.Parse(*body.FlowID)
		if err != nil {
			// 畸形 `flow_id` 与"流不存在"同码：不向客户端泄漏解析细节。
			h.fail(w, r, ErrFlowNotFound)
			return
		}
		flowID = parsed
	}

	issued, err := h.orch.BeginChallenge(r.Context(), BeginChallengeInput{
		FlowID:   flowID,
		Factor:   pathFactor(r),
		Purpose:  factor.Purpose(body.Purpose),
		ClientID: body.ClientID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	resp := challengeResponse{
		ChallengeID:  issued.ChallengeID,
		NextAction:   issued.NextAction,
		TargetMasked: issued.TargetMasked,
		AttemptsLeft: issued.AttemptsLeft,
	}
	if issued.FlowID != nil {
		id := issued.FlowID.String()
		resp.FlowID = &id
	}
	if !issued.ExpiresAt.IsZero() {
		expires := issued.ExpiresAt
		resp.ExpiresAt = &expires
		if ttl := int64(time.Until(expires).Seconds()); ttl > 0 {
			resp.TTLSeconds = ttl
		}
	}
	writeJSON(w, h.logger, http.StatusOK, resp)
}

func (h *HTTPHandler) verify(w http.ResponseWriter, r *http.Request) {
	var body verifyRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	flowID, err := uuid.Parse(strings.TrimSpace(body.FlowID))
	if err != nil {
		h.fail(w, r, ErrFlowNotFound)
		return
	}

	res, err := h.orch.VerifyChallenge(r.Context(), VerifyInput{
		FlowID:      flowID,
		Factor:      pathFactor(r),
		ChallengeID: body.ChallengeID,
		Response:    body.Response,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 达标才算"这一步完成"；未达标时 `ACR_INSUFFICIENT`（403 + details.flow）已在 fail 里给出，
	// 这里只可能是 satisfied（编排层的 `ACRInsufficientError` 走的是错误分支）。
	resp := verifyResponse{Verified: true, ACR: res.Flow.CurrentACR, AMR: achievedAMR(res.Flow)}
	descriptor := res.Flow
	resp.Flow = &descriptor
	writeJSON(w, h.logger, http.StatusOK, resp)
}

func (h *HTTPHandler) describe(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("flow_id"))
	if raw == "" {
		httpx.WriteError(w, traceID(r), httpx.CodeMissingParameter, "缺少 flow_id", nil)
		return
	}
	flowID, err := uuid.Parse(raw)
	if err != nil {
		h.fail(w, r, ErrFlowNotFound)
		return
	}
	d, err := h.orch.Describe(r.Context(), flowID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, h.logger, http.StatusOK, d)
}

// fail 把编排层的错误写成统一的四字段错误响应。
//
// 唯一的特殊分支是 `ACR_INSUFFICIENT`：它**不是失败**而是引导（索引 §2 原则 3），
// 必须把完整 Descriptor 放进 `details.flow`，前端据此渲染补步（AC-FLOW-02）。
func (h *HTTPHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var insufficient *ACRInsufficientError
	if errors.As(err, &insufficient) {
		httpx.WriteErrorStatus(w, traceID(r), httpx.CodeACRInsufficient, http.StatusForbidden,
			"当前档位不足，请完成剩余步骤", map[string]any{"flow": insufficient.Flow})
		return
	}
	// **完整性失败先单独处理**（D15④ / ADR-005）：它被 `CodeOf` 归到 `INTERNAL_ERROR`，
	// 若不在这里拦一道，就只剩一句"服务内部错误，请稍后重试"——而这类失败是**永久性缺陷**，
	// 客户端按 5xx 重试会对"存量 v1 未重加密"打出重试风暴。
	if errors.Is(err, credential.ErrIntegrityMismatch) {
		sentinel := httpx.CryptoIntegritySentinel(err)
		total := httpx.IncIntegrityMismatch()
		// 结构化日志是这套哨兵的**唯一出口**（指标/告警底座归 S10，见 httpx 的缺件说明）。
		h.logger.ErrorContext(r.Context(), "flow: envelope integrity check failed",
			"trace_id", traceID(r), "path", r.URL.Path, "code", string(httpx.CodeInternalError),
			"crypto_sentinel", sentinel, "retryable", false,
			"metric", "integrity_mismatch_total", "count", total)
		httpx.WriteErrorStatus(w, traceID(r), httpx.CodeInternalError, http.StatusInternalServerError,
			httpx.IntegrityMessage, map[string]any{"crypto_sentinel": sentinel, "retryable": false})
		return
	}
	code, okCode := CodeOf(err)
	status, okStatus := StatusFor(err)
	if !okCode || !okStatus {
		// 不认识的错误：详细原因只进日志（按 trace_id 可查），对外统一 500。
		h.logger.ErrorContext(r.Context(), "flow: unhandled error",
			"trace_id", traceID(r), "path", r.URL.Path, "error", err)
		httpx.WriteError(w, traceID(r), httpx.CodeInternalError, "服务内部错误，请稍后用同一 trace_id 重试", nil)
		return
	}
	httpx.WriteErrorStatus(w, traceID(r), code, status, messageFor(code), nil)
}

// messageFor 给码配一句面向用户的提示（可复制 trace_id 才是排障入口，文案不承载细节）。
func messageFor(c httpx.Code) string {
	switch c {
	case httpx.CodeMissingParameter:
		return "缺少必要的参数"
	case httpx.CodeInvalidParam:
		return "参数不合法"
	case httpx.CodeFlowNotFound:
		return "登录流程不存在或已结束，请重新开始"
	case httpx.CodeFlowExpired:
		return "登录流程已超时，请重新开始"
	case httpx.CodeChallengeInvalid:
		return "验证请求无效，请重新获取"
	case httpx.CodeChallengeExpired:
		return "验证已超时，请重新获取"
	case httpx.CodeChallengeConsumed:
		return "验证已使用过，请重新获取"
	case httpx.CodeAttemptsExceeded:
		return "尝试次数过多，请重新获取验证"
	case httpx.CodeFactorDisabled:
		return "该业务系统未开放这种登录方式"
	case httpx.CodeFactorNotSupported:
		return "平台不支持这种登录方式"
	case httpx.CodeNoEligibleFactor:
		return "没有可用的登录方式"
	case CodeStepAlreadySatisfied:
		return "该步骤已完成，无需重复验证"
	case httpx.CodeInvalidCredential:
		return "账号或凭据不正确"
	default:
		return "请求无法完成"
	}
}

// decodeJSON 读并解码请求体；失败时已写出错误响应，返回 false。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// 超过上限是"请求太大"，不是"格式错"：两者混在一个码里会让前端改错方向。
			httpx.WriteError(w, traceID(r), httpx.CodeInvalidRequest, "请求体过大", nil)
			return false
		}
		httpx.WriteError(w, traceID(r), httpx.CodeInvalidRequest, "请求体不是合法 JSON", nil)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// 头已发出，改不了状态码：只能留日志（多半是客户端断开）。
		logger.Warn("flow: write response failed", "error", err)
	}
}

// pathFactor 取路径里的因子类型（`/auth/challenge/{factor}`）。
func pathFactor(r *http.Request) factor.FactorType {
	return factor.FactorType(r.PathValue("factor"))
}

func traceID(r *http.Request) string { return httpx.TraceIDFromContext(r.Context()) }

// achievedAMR 把 Descriptor 里 status=done 的步骤折回 amr 取值（响应 §3.2.2 的 `amr[]`）。
//
// 折算走 `factor.AMRFor`（附录 B 词表只有一处），不在 handler 里手写 `pwd`/`totp`。
func achievedAMR(d Descriptor) []string {
	out := make([]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		if s.Status != StepDone {
			continue
		}
		if amr, ok := factor.AMRFor(s.Factor); ok {
			out = append(out, amr)
		}
	}
	return out
}
