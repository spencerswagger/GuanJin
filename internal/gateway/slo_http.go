package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件是 **R12 = 候选 A（最小投递契约）** 的落地：单点登出（SLO）通知的 HTTP 投递。
//
// 口径（Spec 裁定候选 A；`decisions.md` D16③ / CH-9 已按「对外可见新 wire」登记，字段仍待 R12 定稿）：
//   - 路径：`GUANJIN_SLO_NOTIFY_PATH`，默认 **`/internal/guanjin/v1/logout`**；
//   - 超时：**1000ms**；重试：**1 次**（共 2 次尝试）；
//   - 报文：**复用断言的十项 claim**，装在 `X-GuanJin-Assertion` 头里（不新增 claim、不加开关键）；
//   - 目标：**路由表里该 client 的 `upstream`**（不去猜域名、不做服务发现——索引 §4.2-18 的静态映射）。
//
// 判据语义（`AC-DP-12`）：「通知失败不阻塞本地撤销」——投递成功与否都不改变撤销结果，
// 由 `SloBroadcaster` 兜住（本文件只负责"一次投递怎么写"，不负责"失败怎么办"）。
//
// 不做什么（明确写下来，防"顺手加）：
//   - **不加** `enabled`/`timeout`/`retries` 之类的配置键（A 是默认口径，不是可调开关）；
//   - **不加**新 claim（十项就是断言的全部；多一项就是新增契约面）；
//   - **不碰** `migrations/`（CH-9 只登记 wire，不定义字段/表）。

// 候选 A 的默认值（Spec 裁定；改它们等于改 wire 契约）。
const (
	DefaultSloNotifyPath = "/internal/guanjin/v1/logout"
	// SloDeliveryTimeout 是**单次投递**上限（候选 A 的 1000ms）；广播器的整体兜底仍是
	// `DefaultSloTimeout`（5s），两者不是一回事：前者管"这一次 POST 多久放弃"，
	// 后者管"调用方最多等多久"。
	SloDeliveryTimeout    = 1000 * time.Millisecond
	DefaultSloRetries     = 1
	sloNotifyBodyMaxBytes = 4 << 10
)

// ErrSloNoUpstream 表示路由表里没有该 client 的上游（配置缺失 → 这条通知投不出去）。
var ErrSloNoUpstream = errors.New("gateway: no upstream configured for client")

// UpstreamLookup 按 `client_id` 取路由表里的上游基址（装配层用 `Router` 折出来）。
type UpstreamLookup func(clientID string) (*url.URL, bool)

// HTTPNotifierConfig 是候选 A 的装配参数。**只允许改路径/客户端**，超时与重试按 A 的常量走。
type HTTPNotifierConfig struct {
	// Path 为空时取 `DefaultSloNotifyPath`。
	Path string
	// Client 为 nil 时用带 `DefaultSloTimeout` 超时的客户端。
	Client *http.Client
	Logger *slog.Logger
}

// HTTPNotifier 是候选 A 的投递实现。
type HTTPNotifier struct {
	path      string
	client    *http.Client
	signer    *AssertionSigner
	upstreams UpstreamLookup
	logger    *slog.Logger
}

// NewHTTPNotifier 构造投递器。`signer` 与 `upstreams` 必填（缺了就投不出可验签的通知）。
func NewHTTPNotifier(signer *AssertionSigner, upstreams UpstreamLookup, cfg HTTPNotifierConfig) (*HTTPNotifier, error) {
	if signer == nil {
		return nil, errors.New("gateway: slo notifier requires assertion signer")
	}
	if upstreams == nil {
		return nil, errors.New("gateway: slo notifier requires upstream lookup")
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = DefaultSloNotifyPath
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("gateway: slo notify path must start with '/': %q", path)
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: SloDeliveryTimeout}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPNotifier{path: path, client: client, signer: signer, upstreams: upstreams, logger: logger}, nil
}

// Notify 投递一条通知（`notice.ClientIDs` 由 `SloBroadcaster` 逐个投递）。
//
// 报文：空 JSON 体（撤销事实全在断言 claim 里——`sub`/`aud`/`jti`/`iat`/`exp`；
// 多塞一份 body 等于造两套事实来源）。`Idempotency-Key` 用 `jti`，让业务方按它去重。
func (n *HTTPNotifier) Notify(ctx context.Context, notice RevocationNotice) error {
	if n == nil {
		return errors.New("gateway: nil slo notifier")
	}
	if len(notice.ClientIDs) != 1 {
		return fmt.Errorf("gateway: slo notify expects exactly one client, got %d", len(notice.ClientIDs))
	}
	clientID := strings.TrimSpace(notice.ClientIDs[0])
	if clientID == "" {
		return errors.New("gateway: slo notify requires client_id")
	}
	source := notice.Subject
	if source == nil {
		// 没有身份事实就签不出可验签的通知：如实报错（由 broadcaster 计失败、不阻塞撤销）。
		return errors.New("gateway: slo notify requires subject facts (uname/roles/acr/amr/cid)")
	}
	upstream, ok := n.upstreams(clientID)
	if !ok || upstream == nil {
		return fmt.Errorf("%w: %s", ErrSloNoUpstream, clientID)
	}

	in := *source
	in.Aud = clientID // `aud` 必须是**目标业务系统**（D16① 同一条规则）
	assertion, err := n.signer.Sign(in)
	if err != nil {
		return fmt.Errorf("gateway: sign slo assertion: %w", err)
	}

	endpoint := *upstream
	endpoint.Path = singleJoiningSlash(strings.TrimSuffix(upstream.Path, "/"), n.path)

	var lastErr error
	// 重试 `DefaultSloRetries` 次（共 1+1 次尝试）：网络抖动不该让业务方漏掉一次登出通知。
	for attempt := 0; attempt <= DefaultSloRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("gateway: slo notify cancelled: %w", err)
		}
		lastErr = n.deliverOnce(ctx, endpoint.String(), assertion.Token)
		if lastErr == nil {
			return nil
		}
		n.logger.Warn("slo: notify attempt failed",
			"error", lastErr, "client_id", clientID, "attempt", attempt+1, "endpoint", endpoint.String())
	}
	return lastErr
}

// deliverOnce 是一次投递（不含重试）。
func (n *HTTPNotifier) deliverOnce(ctx context.Context, endpoint, assertion string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(nil))
	if err != nil {
		return fmt.Errorf("gateway: build slo request: %w", err)
	}
	if err := Apply(req, assertion); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// 报文体留空：撤销事实全在断言 claim 里（`sub`/`aud`/`jti`/`iat`/`exp`），
	// 多塞一份 body 就是两套事实来源。业务方按断言里的 `jti` 去重。
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway: slo request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 只读掉小体量响应，避免连接无法复用；不解析、不落日志（防把业务方内部结构写进我们日志）。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, sloNotifyBodyMaxBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway: slo notify got status %d", resp.StatusCode)
	}
	return nil
}

// UpstreamsFromRouter 把路由表折成 `client → upstream` 的查找（同一 client 只取第一条，
// 装配期路由表已保证前缀唯一；`client_id` 重复属部署配置问题，取首条并日志由装配层负责）。
//
// 放在这里而不是让调用方自己遍历：候选 A 说"目标取路由表该 client 的 upstream"，
// 这句话只应有一处实现，否则网关与 SLO 会各自解读"该 client 的上游是哪台"。
func UpstreamsFromRouter(r *Router) UpstreamLookup {
	byClient := make(map[string]*url.URL, len(r.Routes()))
	for _, route := range r.Routes() {
		if _, exists := byClient[route.ClientID]; exists {
			continue
		}
		byClient[route.ClientID] = route.Upstream
	}
	return func(clientID string) (*url.URL, bool) {
		u, ok := byClient[clientID]
		return u, ok
	}
}
