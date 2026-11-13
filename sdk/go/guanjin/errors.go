package guanjin

import (
	"errors"
	"net/http"
)

// 错误码（契约 §4.1 M4 / §4.4 fail-closed 矩阵）：**只消费接口清单 §5.4**，不自造码。
const (
	// CodeTokenInvalid：内部断言校验失败的**唯一**对外码（缺头 / 验签失败 / claim 越界 /
	// jti 重放 / iss·aud 不符都归它）——故意不区分"过期"与"伪造"（契约 §4.4 末表）。
	CodeTokenInvalid = "TOKEN_INVALID"
	// CodeKeyUnavailable：JWKS 不可得且无本地缓存（验签公钥拿不到 = 无法确认身份）。
	CodeKeyUnavailable = "KEY_UNAVAILABLE"
	// CodeRedisUnavailable：jti 存储（replay-store=redis）不可用。
	CodeRedisUnavailable = "REDIS_UNAVAILABLE"
	// CodeInsufficientRole：本地判定未通过 / 模型完整性校验失败（本版本未实现判定）。
	CodeInsufficientRole = "INSUFFICIENT_ROLE"
)

// 错误族（sentinel，便于调用方 fail-closed 分支）。
var (
	// ErrTokenInvalid 表示内部断言不可信（一律 401）。
	ErrTokenInvalid = errors.New("guanjin: token invalid")
	// ErrKeyUnavailable 表示 JWKS 不可得且没有可用缓存（一律 503 KEY_UNAVAILABLE）。
	ErrKeyUnavailable = errors.New("guanjin: verification key unavailable")
	// ErrReplayUnavailable 表示 jti 存储不可用（一律 503 REDIS_UNAVAILABLE，fail-closed）。
	ErrReplayUnavailable = errors.New("guanjin: replay store unavailable")
	// ErrNotImplemented 表示该能力在**本版本**未实现（显式标注，不静默缺；见 sdk/README.md）。
	ErrNotImplemented = errors.New("guanjin: capability not implemented in this build")
)

// Error 是带对外码的 SDK 错误。
type Error struct {
	Code    string // 对外码（接口清单 §5.4）
	Status  int    // HTTP 状态
	Message string // 面向人的说明（不含敏感信息）
	Err     error  // 底层原因（只进日志）
}

// Error 实现 error。
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is 链到 sentinel。
func (e *Error) Unwrap() error { return e.Err }

// NewTokenInvalid 构造 401 TOKEN_INVALID。
func NewTokenInvalid(msg string, cause error) *Error {
	return &Error{Code: CodeTokenInvalid, Status: http.StatusUnauthorized, Message: msg, Err: wrap(ErrTokenInvalid, cause)}
}

// NewKeyUnavailable 构造 503 KEY_UNAVAILABLE。
func NewKeyUnavailable(msg string, cause error) *Error {
	return &Error{Code: CodeKeyUnavailable, Status: http.StatusServiceUnavailable, Message: msg, Err: wrap(ErrKeyUnavailable, cause)}
}

// NewReplayUnavailable 构造 503 REDIS_UNAVAILABLE。
func NewReplayUnavailable(msg string, cause error) *Error {
	return &Error{Code: CodeRedisUnavailable, Status: http.StatusServiceUnavailable, Message: msg, Err: wrap(ErrReplayUnavailable, cause)}
}

func wrap(sentinel, cause error) error {
	if cause == nil {
		return sentinel
	}
	return errors.Join(sentinel, cause)
}
