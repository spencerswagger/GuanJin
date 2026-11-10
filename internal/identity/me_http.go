package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **`AC-FLOW-09`** 的端点面：`POST /api/v1/me/identifiers`（接口清单 :85）。
//
// 判据只挂这一个端点（`GET/DELETE/verify` 在 `acceptance-criteria.md` 里 0 命中，本切片不做，
// 但"端点落地清单"里要如实列缺——`DELETE` 的鉴权列同样写着"生效配置 ACR"）。
//
// 判定链（无 `client_id` 上下文，所以**不取任何 `client_override`**）：
//
//	当前 ACR = 令牌所属会话的 `sessions.acr`
//	目标 ACR = (user_visited_clients ⨝ clients(status='active')) 的 max tier → tier_default.min_acr
//	不足       → **403 `ACR_INSUFFICIENT`**，`details` 只给平铺 `current_acr`/`target_acr`，**不带 `details.flow`**
//
// 为什么 403 不带 `details.flow`：`flows.client_id NOT NULL`，无上下文开不出流；硬给一个
// `flow_id` 会把客户端引去调 `/auth/challenge`（那条路需要 client），等于引进死路。
// 该口径登记在 D18 草案，**待 spencer 拍板后转正式**（本 PR 不自宣生效）。

// IdentifierBinder 是绑定标识符的能力面（实现是 `*identity.Service`）。
type IdentifierBinder interface {
	BindIdentifier(ctx context.Context, req BindRequest) (*model.Identifier, error)
}

// SessionACRReader 读会话行（取 `acr` 作为"当前级别"）。
type SessionACRReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Session, error)
}

// MeIdentifiersHTTP 是 `POST /api/v1/me/identifiers` 的处理器。
type MeIdentifiersHTTP struct {
	binder   IdentifierBinder
	sessions SessionACRReader
	gate     *ACRGate
	resolver session.TokenResolver
	logger   *slog.Logger
}

// NewMeIdentifiersHTTP 构造处理器（缺依赖即装配期失败）。
func NewMeIdentifiersHTTP(binder IdentifierBinder, sessions SessionACRReader, gate *ACRGate,
	resolver session.TokenResolver, logger *slog.Logger) (*MeIdentifiersHTTP, error) {
	switch {
	case binder == nil, sessions == nil, gate == nil, resolver == nil, logger == nil:
		return nil, errors.New("identity: me/identifiers requires all dependencies")
	}
	return &MeIdentifiersHTTP{binder: binder, sessions: sessions, gate: gate, resolver: resolver, logger: logger}, nil
}

// Routes 挂上本切片唯一的端点。
func (h *MeIdentifiersHTTP) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/me/identifiers", h.bind)
}

// bindRequest 是请求体（接口清单 :85：绑定新标识符并发起验证）。
type bindRequest struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// bindResponse 只回"新建了什么"，不回标识符明文（脱敏是列表接口的事，这里连回显都不给）。
type bindResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Verified  bool   `json:"verified"`
	TargetACR string `json:"target_acr,omitempty"`
}

func (h *MeIdentifiersHTTP) bind(w http.ResponseWriter, r *http.Request) {
	caller, ok := session.ResolveCaller(w, r, h.resolver)
	if !ok {
		return
	}

	var body bindRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeInvalidRequest, "请求体不是合法 JSON", nil)
		return
	}
	if body.Type == "" || body.Value == "" {
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeMissingParameter, "缺少 type 或 value", nil)
		return
	}

	// ① 当前级别：令牌所属会话的 acr（**不**信任客户端自报）。
	//    会话不存在/已撤销 → 401（令牌不可信），与"档位不足"分开。
	sess, err := h.sessions.GetByID(r.Context(), caller.SessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeTokenInvalid, "令牌无效或已过期", nil)
			return
		}
		h.fail(w, r, err)
		return
	}
	current := factor.ACR(sess.ACR)

	// ② 目标级别：访问过业务系统的最高档位 → 该档位的默认门槛（无上下文，不看 override）。
	target, err := h.gate.Target(r.Context(), caller.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// ③ 不足 → 403 + 平铺 current/target（**不带 details.flow**，见文件头）。
	if gateErr := h.gate.Check(current, target); gateErr != nil {
		var insuff *ACRInsufficientError
		if errors.As(gateErr, &insuff) {
			httpx.WriteErrorStatus(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeACRInsufficient, http.StatusForbidden,
				"当前会话级别不足，无法执行该操作",
				map[string]any{
					"current_acr": string(insuff.Current),
					"target_acr":  string(insuff.Target),
					"tier":        insuff.Tier,
				})
			return
		}
		h.fail(w, r, gateErr)
		return
	}

	// ④ 达标才绑定（新标识符一律 `verified=false`，走 channel 验证后方可用）。
	created, err := h.binder.BindIdentifier(r.Context(), BindRequest{
		UserID: caller.UserID,
		Type:   body.Type,
		Value:  body.Value,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeRegisterJSON(w, h.logger, http.StatusCreated, bindResponse{
		ID:        created.ID.String(),
		Type:      created.Type,
		Verified:  created.Verified,
		TargetACR: string(target.ACR),
	})
}

func (h *MeIdentifiersHTTP) fail(w http.ResponseWriter, r *http.Request, err error) {
	if code, ok := CodeOf(err); ok {
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), code, identityMessage(code), nil)
		return
	}
	h.logger.ErrorContext(r.Context(), "identity: unhandled me/identifiers error",
		"trace_id", httpx.TraceIDFromContext(r.Context()), "path", r.URL.Path, "error", err)
	httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeInternalError, "服务内部错误，请稍后重试", nil)
}

func identityMessage(c httpx.Code) string {
	switch c {
	case httpx.CodeIdentifierAlreadyBound:
		return "该标识符已被绑定"
	case httpx.CodeIdentifierInvalidFormat:
		return "标识符格式不合法"
	case httpx.CodeInvalidParam, httpx.CodeMissingParameter:
		return "请求参数不合法"
	case httpx.CodeACRInsufficient:
		return "当前会话级别不足，请先完成更强认证"
	default:
		return "请求无法完成"
	}
}
