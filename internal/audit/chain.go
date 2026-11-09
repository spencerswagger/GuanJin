package audit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件是**审计哈希链的纯计算部分**（AC-AUD-02/03/04/05 的核心）：
// 规范化编码 + `HMAC-SM3` 计算 + 链校验。**不碰数据库**（落库/锁/扫表在 `repo.go`），
// 这样"链断在哪一条"可以用假仓储离线钉住，而真库用例只验"SQL 与并发真的按预期跑"。
//
// 口径（数据模型 §3.6 / D4-C / 索引 §4.2-19）：
//   - **单全局链**：`prev_tag` = 上一条的 `integrity_tag`（首条为 `nil`，由锚点接续）；
//   - `integrity_tag = HMAC-SM3(K_audit, prev_tag || 规范化记录)`，`K_audit` 由主密钥派生
//     （用途域 `guanjin/hmac/audit`，D4-B 的封闭 8 值之一）；
//   - **规范化必须逐字节确定**：JSON 的 map 顺序、时区、数组顺序都会让"同一行"算出不同的 tag，
//     那样链会在**没有任何篡改**的情况下"自己断掉"。

// ErrChainBroken 表示链在某一条上对不上（`prev_tag` 不等于上一条的 `integrity_tag`，
// 或 `integrity_tag` 与按内容重算的结果不符）。
var ErrChainBroken = errors.New("audit: chain is broken")

// Record 是一条待写/待校验的审计事实（与 `audit_logs` 的列一一对应，时间用 UTC）。
type Record struct {
	ID          uuid.UUID
	ActorUserID *uuid.UUID
	ActorRole   string
	Action      string
	TargetType  string
	TargetID    string
	ClientID    string
	Before      map[string]any
	After       map[string]any
	Result      string
	IP          string
	TraceID     string
	OccurredAt  time.Time
	// Signature 是"关键操作"的 SM2 签名（可空；不参与链计算——它签的是链上已有的事实，
	// 若把它也纳入链，签名轮换会连带改链，得不偿失）。
	Signature []byte

	// PrevTag / IntegrityTag 是链上的两段：读出来的既有值 / 算出来的值。
	PrevTag      []byte
	IntegrityTag []byte
}

// Chain 是链计算器（持派生好的 HMAC 键；并发只读安全）。
//
// 键类型是 `crypto.HMACKey`（字段不导出、只能由主密钥派生）：审计包**拿不到原始密钥字节**，
// 只能把"算法入口"当作唯一用法——这与 `internal/crypto` 的既有纪律一致（D4-B）。
type Chain struct {
	// afterTailRead 是**仅测试用**的注入点（生产恒为 nil）：在"读到链尾"与"插入"之间
	// 插入可控延迟，用来证明并发防线真的有效。见 `repo.go` 的 `Append`。
	afterTailRead func()

	// disableChainLock 是**仅测试用**的反证开关（生产恒为 false）：跳过 advisory 锁，
	// 用来证明"去掉串行化后探针确实能检测到分叉"——否则"零分叉"可能只是探针失灵。
	disableChainLock bool

	key crypto.HMACKey
}

// NewChain 绑定审计链的 HMAC 键（调用方从主密钥派生 `crypto.PurposeHMACAudit`）。
func NewChain(key crypto.HMACKey) (*Chain, error) {
	if key.IsZero() {
		return nil, errors.New("audit: chain requires a non-zero HMAC key (derive crypto.PurposeHMACAudit)")
	}
	return &Chain{key: key}, nil
}

// Tag 计算某条记录的 `integrity_tag`：`HMAC-SM3(key, prevTag || canonical(record))`。
//
// `prevTag` 为 nil 表示这是链首（或某个分段的起点，分段由锚点 `segment.<YYYYMM>` 标定）。
func (c *Chain) Tag(prevTag []byte, r Record) ([]byte, error) {
	body, err := canonical(r)
	if err != nil {
		return nil, err
	}
	if c == nil || c.key.IsZero() {
		return nil, errors.New("audit: nil chain")
	}
	// `prevTag || canonical`：显式复制，别用 append 复用 prevTag 的底层数组（会悄悄改调用方的切片）。
	input := make([]byte, 0, len(prevTag)+len(body))
	input = append(input, prevTag...)
	input = append(input, body...)
	return crypto.HMACSM3(c.key, input), nil
}

// Verify 从 `records`（**必须按 `occurred_at, id` 升序**）校验链。
//
// `anchor` 是分段起点的 `prev_tag`（链首为 nil）；返回第一个断点（nil = 整链通过）。
// 断点判定有**两种**，缺一不可：
//  1. `rec.PrevTag` 必须逐字节等于上一条的 `IntegrityTag`（删中间一条/乱序都会在这里暴露）；
//  2. 按内容重算的 tag 必须等于 `rec.IntegrityTag`（改内容但没改 tag 会在这里暴露）。
func (c *Chain) Verify(records []Record, anchor []byte) (*BrokenPoint, error) {
	// **先判链是否可用**：空记录集也要报错——nil/零键的链若在这里静默返回"通过"，
	// 调用方会拿到"整链完好"的结论，而实际上一次都没校验（最坏形态：校验 Job 变成摆设）。
	if c == nil || c.key.IsZero() {
		return nil, errors.New("audit: nil chain")
	}
	prev := anchor
	for i := range records {
		rec := records[i]
		if !bytesEqual(rec.PrevTag, prev) {
			return &BrokenPoint{
				Index:    i,
				RecordID: rec.ID,
				Reason:   ReasonPrevMismatch,
			}, nil
		}
		want, err := c.Tag(prev, rec)
		if err != nil {
			return nil, err
		}
		if !bytesEqual(want, rec.IntegrityTag) {
			return &BrokenPoint{
				Index:    i,
				RecordID: rec.ID,
				Reason:   ReasonTagMismatch,
			}, nil
		}
		prev = rec.IntegrityTag
	}
	return nil, nil
}

// 断链原因（对外的 `verified|broken` 之外，留一个可定位的原因码；不新增对外错误码）。
const (
	// ReasonPrevMismatch 上一条被删/顺序变了/`prev_tag` 被改。
	ReasonPrevMismatch = "prev_tag_mismatch"
	// ReasonTagMismatch 内容被改（tag 与按内容重算的结果不符）。
	ReasonTagMismatch = "integrity_tag_mismatch"
)

// BrokenPoint 是第一个断点的位置。
type BrokenPoint struct {
	Index    int
	RecordID uuid.UUID
	Reason   string
}

func (b *BrokenPoint) Error() string {
	if b == nil {
		return ""
	}
	return fmt.Sprintf("%v: record %s at index %d (%s)", ErrChainBroken, b.RecordID, b.Index, b.Reason)
}

// Is 让 `errors.Is(broken, ErrChainBroken)` 成立（否则调用方只能比字符串，等于没有哨兵）。
func (b *BrokenPoint) Is(target error) bool { return target == ErrChainBroken }

// canonical 把一条记录编码成**逐字节确定**的字节串。
//
// 编码规则（写死，改它就等于改历史链——**必须**升 ADR 并做迁移）：
//   - 字段顺序固定（下面 Write 的顺序就是顺序），长度前缀（uint32 BE）+ 内容；
//   - 时间一律 `UTC().Format(time.RFC3339Nano)`；
//   - `before`/`after` 两个 JSON 对象按键名字典序递归编码，数组保序；
//   - `nil` 与空串/空对象**必须区分**（用 1 字节 presence 标记）——否则"删掉一个字段"与
//     "字段为空"会算出同一个 tag，那是链的漏洞。
func canonical(r Record) ([]byte, error) {
	var b enc
	b.uuid(r.ID)
	b.optUUID(r.ActorUserID)
	b.str(r.ActorRole)
	b.str(r.Action)
	b.str(r.TargetType)
	b.str(r.TargetID)
	b.str(r.ClientID)
	if err := b.json(r.Before); err != nil {
		return nil, fmt.Errorf("audit: canonical before: %w", err)
	}
	if err := b.json(r.After); err != nil {
		return nil, fmt.Errorf("audit: canonical after: %w", err)
	}
	b.str(r.Result)
	b.str(r.IP)
	b.str(r.TraceID)
	b.str(r.OccurredAt.UTC().Format(time.RFC3339Nano))
	return b.buf, nil
}

// enc 是"长度前缀 + presence 标记"的小编码器（不引序列化库：要的是**确定**，不是通用）。
type enc struct{ buf []byte }

func (e *enc) presence(ok bool) {
	if ok {
		e.buf = append(e.buf, 1)
		return
	}
	e.buf = append(e.buf, 0)
}

func (e *enc) str(s string) {
	e.buf = append(e.buf, make([]byte, 4)...)
	binary.BigEndian.PutUint32(e.buf[len(e.buf)-4:], uint32(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *enc) uuid(id uuid.UUID) {
	e.buf = append(e.buf, id[:]...)
}

func (e *enc) optUUID(id *uuid.UUID) {
	if id == nil {
		e.presence(false)
		return
	}
	e.presence(true)
	e.uuid(*id)
}

func (e *enc) json(v map[string]any) error {
	if v == nil {
		e.presence(false)
		return nil
	}
	e.presence(true)
	return e.value(v)
}

// value 递归编码 JSON 值：对象按键名排序、数组保序、数字统一成 `json.Number` 的规范串。
func (e *enc) value(v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.str("obj")
		e.str(fmt.Sprintf("%d", len(keys)))
		for _, k := range keys {
			e.str(k)
			if err := e.value(t[k]); err != nil {
				return err
			}
		}
		return nil
	case []any:
		e.str("arr")
		e.str(fmt.Sprintf("%d", len(t)))
		for _, item := range t {
			if err := e.value(item); err != nil {
				return err
			}
		}
		return nil
	case string:
		e.str("s")
		e.str(t)
		return nil
	case bool:
		e.str("b")
		if t {
			e.str("1")
		} else {
			e.str("0")
		}
		return nil
	case nil:
		e.str("null")
		return nil
	case json.Number:
		e.str("n")
		e.str(t.String())
		return nil
	case float64:
		// 统一成 `json.Number` 的规范串，避免 `1` 与 `1.0` 算出两个 tag。
		e.str("n")
		e.str(fmt.Sprintf("%v", t))
		return nil
	default:
		// 未知类型：用 JSON 兜底（仍然确定，因为 encoding/json 对同一 Go 值是确定的）。
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		e.str("json")
		e.str(string(raw))
		return nil
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AnchorKey 返回某月锚点在 `runtime_config` 里的键（D4-C：`audit.chain.anchor.<YYYYMM>`）。
func AnchorKey(t time.Time) string {
	return "audit.chain.anchor." + t.UTC().Format("200601")
}

// SegmentKey 返回某月分段标记的键（`audit.chain.segment.<YYYYMM>`）。
func SegmentKey(t time.Time) string {
	return "audit.chain.segment." + t.UTC().Format("200601")
}

// NewID 生成记录主键（UUIDv7，与应用主键同一生成器）。
func NewID() (uuid.UUID, error) { return uuid.NewV7() }
