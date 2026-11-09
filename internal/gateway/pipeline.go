package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件把数据面的 7 件事按**固定顺序**串起来（顺序本身就是契约，改顺序 = 改语义）：
//
//	① 先弃客户端伪造的 `X-GuanJin-Assertion`（AC-DP-02/03：哪怕马上要被拒，也不许透传）
//	② 路由匹配（无路由 → 404 `CLIENT_NOT_FOUND`，不裸转发）
//	③ 准入：验签 → 会话（含实时撤销）→ 档位（401/403/500/503，一律不注入身份）
//	④ 逐请求签断言（一次生成 `iat/exp/jti`，整条链路复用）
//	⑤ 转发（上游失败 → 503 `UPSTREAM_UNAVAILABLE` + `Retry-After`，AC-DP-13）
//
// **`jti` 重放判重不在本进程**（`AC-DP-08` 的平台半边 = 每请求现签新 `jti` + 被拒时零注入；
// 「同 `jti` 二次 → 401」由**验签方**（SDK / S9）产���并记账，见 `数据面设计` §4.6 第 3 步
// 与 `SDK契约` §7）。数据面**不存、不查、不比对** `jti`——`TestGatewayNeverStoresOrComparesJTI`
// 就是这条的结构性守线。
//
// ③④ 之间失败（如签名密钥不可用）按 `KEY_UNAVAILABLE`（503）处理并**不转发**：
// 宁可让这条请求失败，也不让一条没有身份的请求进入业务系统（AC-FC-03 的同一条原则）。
//
// 日志纪律（AC-DP-10）：访问日志只记 `trace_id/method/path/status/duration_ms/client_id`。
// 令牌、断言、口令、标识符明文**一律不进日志**——所以本文件的日志参数里没有它们。

// Pipeline 是数据面的主处理器（**无本地状态**：多副本行为一致）。
type Pipeline struct {
	forward *Handler
	guard   *Guard
	signer  *AssertionSigner
	logger  *slog.Logger
}

// NewPipeline 校验装配完整性：缺路由内核/准入判定/签发器任何一个都不起数据面。
//
// 这不是"防御性编程"：三件里少一件，数据面就退化成"裸反代"或"永远 401"，
// 两种都该在启动期暴露，而不是等第一个请求。
func NewPipeline(forward *Handler, guard *Guard, signer *AssertionSigner, logger *slog.Logger) (*Pipeline, error) {
	switch {
	case forward == nil:
		return nil, errors.New("gateway: pipeline requires a forwarder (nil = 裸转发风险)")
	case guard == nil:
		return nil, errors.New("gateway: pipeline requires a guard (nil = 认证可被绕过)")
	case signer == nil:
		return nil, errors.New("gateway: pipeline requires an assertion signer (nil = 上游拿不到身份)")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{forward: forward, guard: guard, signer: signer, logger: logger}, nil
}

// ServeHTTP 实现 `http.Handler`。
func (p *Pipeline) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ① 先弃：进任何分支之前就删，后面所有消费者（准入、转发、日志）都拿不到伪造值。
	stripAssertionHeader(r.Header)

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	traceID := httpx.TraceIDFromContext(r.Context())
	defer func() {
		p.logger.Info("data-plane request",
			"trace_id", traceID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client_id", rec.clientID,
		)
	}()

	// ② 路由
	//
	// 未匹配用 **404 `CLIENT_NOT_FOUND`**（`decisions.md:416` 定稿；`internal/httpx/errors.go:83`：
	// 该码 E4/404 默认即 404，直接走 `WriteError`、不覆盖状态码），不是 `UPSTREAM_UNAVAILABLE`
	// （后者按 D8-C′④ + AC-DP-13 只给"已开始转发但上游失败/超时"）。
	//
	// **语义边界（防被当通用 404 用）**：`CLIENT_NOT_FOUND` **仅**指「路由 → client 映射缺失」
	// （部署漂移，不是调用方参数错）；其余"找不到"用各自专码，请求本身不合法才用 `INVALID_REQUEST`。
	route, ok := p.forward.router.Match(r.URL.Path)
	if !ok {
		reportMissingRoute(p.logger, traceID, r.URL.Path)
		httpx.WriteError(rec, traceID, httpx.CodeClientNotFound,
			"该路径未归属任何业务系统", nil)
		return
	}
	rec.clientID = route.ClientID

	// ③ 准入
	admission, err := p.guard.Admit(r.Context(), r, route)
	if err != nil {
		p.writeAdmitError(rec, traceID, err)
		return
	}

	// ④ 逐请求签发（每请求一次；重试转发不得重签）
	assertion, err := p.signer.Sign(AssertionFor(admission))
	if err != nil {
		// 签不出来 = 密钥不可用（D8-C：`KEY_UNAVAILABLE` + 503 + `Retry-After`），
		// **不转发**：放过去就等于让业务系统在没有身份的情况下放行。
		p.logger.Error("data-plane: sign assertion failed",
			"error", err, "trace_id", traceID, "client_id", route.ClientID)
		httpx.WriteError(rec, traceID, httpx.CodeKeyUnavailable, "签名服务暂不可用，请稍后重试", nil)
		return
	}
	if err := Apply(r, assertion.Token); err != nil {
		p.logger.Error("data-plane: apply assertion failed",
			"error", err, "trace_id", traceID, "client_id", route.ClientID)
		httpx.WriteError(rec, traceID, httpx.CodeInternalError, "服务内部错误，请携带 trace_id 联系支持", nil)
		return
	}

	// ⑤ 转发
	p.forward.ServeRoute(rec, r, route)
}

// writeAdmitError 把准入失败写成统一四字段响应（+ `details.flow`，仅挡位不足时）。
//
// 细节只进日志：`Internal` 里可能带会话 id、配置漂移等内部事实，进响应等于送情报。
func (p *Pipeline) writeAdmitError(w http.ResponseWriter, traceID string, err error) {
	var admitErr *AdmitError
	if !errors.As(err, &admitErr) {
		// 不该发生：Admit 只返回 *AdmitError。兜底也必须是"拒服"，不能"放过去"。
		p.logger.Error("data-plane: unexpected admit error", "error", err, "trace_id", traceID)
		httpx.WriteError(w, traceID, httpx.CodeInternalError, "服务内部错误，请携带 trace_id 联系支持", nil)
		return
	}
	if admitErr.Status >= http.StatusInternalServerError {
		p.logger.Error("data-plane: admit failed", "code", string(admitErr.Code),
			"error", admitErr.Internal, "trace_id", traceID)
	} else {
		p.logger.Info("data-plane: request denied", "code", string(admitErr.Code),
			"reason", admitErr.Internal, "trace_id", traceID)
	}
	httpx.WriteErrorStatus(w, traceID, admitErr.Code, admitErr.Status, admitErr.Message, admitErr.Details)
}
