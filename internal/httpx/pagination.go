package httpx

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// 分页参数的唯一解析处（D3-A′：`limit` + `cursor`，**不返回 total**）。
//
// 三个刻意的约定：
//  1. **默认 50**：来自接口 §5.5 的"默认 50、上限 200"；D3-A′ 只改了形态
//     （`page/page_size/total` → `limit/cursor`）与超限语义（`limit > 200` → `PAGE_SIZE_EXCEEDED`），
//     没有改默认值。默认值写在**这一处**，不允许各 handler 各写一个。
//  2. **`page`/`page_size` 一律不认**（CH-3 已作废这两个参数）：传了不报错、也不生效，
//     避免"看起来分页了其实没分"。
//  3. `limit` 非数字 / <= 0 → `INVALID_PARAMETER`；> 200 → `PAGE_SIZE_EXCEEDED`。
const (
	// DefaultPageLimit 是列表默认页大小（接口 §5.5）。
	DefaultPageLimit = 50
	// MaxPageLimit 是页大小上限（D3-A′：`limit` > 200 → `PAGE_SIZE_EXCEEDED`）。
	MaxPageLimit = 200
)

var (
	// ErrInvalidLimit 表示 `limit` 不是正整数（对外 `INVALID_PARAMETER`）。
	ErrInvalidLimit = errors.New("httpx: invalid limit")
	// ErrPageSizeExceeded 表示 `limit` 超过上限（对外 `PAGE_SIZE_EXCEEDED`）。
	ErrPageSizeExceeded = errors.New("httpx: page size exceeded")
)

// PageQuery 是解析后的分页参数（`Cursor` 原样透传给仓储，不在这里解码）。
type PageQuery struct {
	Limit  int
	Cursor string
}

// ParsePage 解析 `?limit=&cursor=`。
func ParsePage(r *http.Request) (PageQuery, error) {
	q := PageQuery{Limit: DefaultPageLimit}
	if r == nil {
		return q, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return q, ErrInvalidLimit
		}
		if n > MaxPageLimit {
			return q, ErrPageSizeExceeded
		}
		q.Limit = n
	}
	q.Cursor = strings.TrimSpace(r.URL.Query().Get("cursor"))
	return q, nil
}

// CodeOfPageError 把分页解析错误映射成冻结码。
func CodeOfPageError(err error) Code {
	switch {
	case errors.Is(err, ErrPageSizeExceeded):
		return CodePageSizeExceeded
	case errors.Is(err, ErrInvalidLimit):
		return CodeInvalidParam
	default:
		return ""
	}
}
