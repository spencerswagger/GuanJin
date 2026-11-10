package audit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
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

	// disableChainLock 是**仅测试用**的反证开关（生产恒为 false）：跳过 advisory 锁。
	// 配合 `disableRowLock` 做 b1（只摘 advisory）/ b2（两道都摘）两段反证。
	disableChainLock bool

	// disableRowLock 是**仅测试用**的反证开关（生产恒为 false）：去掉链尾行的 `FOR UPDATE`。
	disableRowLock bool

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
	return c.VerifyFrom(records, anchor, true)
}

// VerifyFrom 与 `Verify` 相同，但可以**跳过首条的 `prev_tag` 校验**（`checkFirst=false`）。
//
// 为什么要这个开关：链按自然月分段，段首的 `prev_tag` 应当等于**上个月封锚的链尾 tag**。
// 如果上个月还没有锚点（历史月、或还没跑过复核），段首的 `prev_tag` 是"未知"而不是"应为 nil"——
// 把"未知"当成 nil 判，会让非首月的首次复核恒报 `prev_tag_mismatch`（Owl II P0 的另一面）。
func (c *Chain) VerifyFrom(records []Record, anchor []byte, checkFirst bool) (*BrokenPoint, error) {
	// **先判链是否可用**：空记录集也要报错——nil/零键的链若在这里静默返回"通过"，
	// 调用方会拿到"整链完好"的结论，而实际上一次都没校验（最坏形态：校验 Job 变成摆设）。
	if c == nil || c.key.IsZero() {
		return nil, errors.New("audit: nil chain")
	}
	prev := anchor
	for i := range records {
		rec := records[i]
		if i == 0 && !checkFirst {
			// 段首 prev_tag 未知：不判它，但仍按内容重算 tag（改内容照样能发现）。
			want, err := c.Tag(rec.PrevTag, rec)
			if err != nil {
				return nil, err
			}
			if !bytesEqual(want, rec.IntegrityTag) {
				return &BrokenPoint{Index: 0, RecordID: rec.ID, Reason: ReasonTagMismatch}, nil
			}
			prev = rec.IntegrityTag
			continue
		}
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
	// ReasonAnchorMismatch 本月封条（`anchor[M]`）在段内**找不到对应记录** ——
	// 典型形态是"整月尾部被删"（链内每一对 prev/tag 仍自洽，只有锚点能发现）。
	// 单独一个原因码，避免把运维引去查一条无辜的最后记录。
	ReasonAnchorMismatch = "anchor_mismatch"
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

// numberText 把 Go 数值转成**与读侧（`encoding/json`）完全一致**的文本。
//
// 返回 ok=false 表示"不是数字"，交给调用方走其它分支。
func numberText(v any) (string, bool, error) {
	switch t := v.(type) {
	case json.Number:
		return t.String(), true, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		raw, err := json.Marshal(t)
		if err != nil {
			return "", false, fmt.Errorf("audit: canonical number: %w", err)
		}
		return string(raw), true, nil
	}
	return "", false, nil
}

// canonicalNumber 是**数字的唯一规范口径**：读（JSON 文本）写（Go 值转出的文本）共用同一个函数。
//
// 入参是"最像 JSON 的文本"——写侧负责把 Go 值转成这种文本（整型用 `FormatInt/FormatUint`，
// `float32` 用 `bitsize=32` 的最短表示，`float64` 用 `bitsize=64`），读侧给 JSON 原文。
// 输出规则按序：
//
//  1. 能 `ParseInt` → 定点十进制（保精度，雪花 ID / 毫秒时间戳不会被抹平）；
//  2. 能 `ParseUint` → 定点十进制；
//  3. 解析不出数（如 `1e400`）→ 原文（仍然确定）；
//  4. `float` 值是整数值且在 int64 范围内 → 定点十进制
//     （`float64(1e18)` 的文本 `1e+18` 与读侧文本 `1000000000000000000` 因此归到同一串）；
//  5. 非负且在 uint64 范围内 → 定点十进制（覆盖 `2^63..2^64` 那段）；
//  6. 其余 → 最短可逆浮点 `'g'`（`1e21` 两侧都停在 `1e+21`）。
//
// **边界不是 2^53**：`v == math.Trunc(v)` 在任何量级都精确；真正的边界是 int64/uint64 的取值域。
// （上两轮把边界写成 `|v| < 2^53` 是错的：`2^53` 本身被排除在外，而它用 `FormatInt` 完全精确，
// 于是 `float64(2^53)`、`1e18` 这类整数在写侧走 `'g'`、读侧走十进制 → 链自断。）
func canonicalNumber(txt string) string {
	if n, err := strconv.ParseInt(txt, 10, 64); err == nil {
		return strconv.FormatInt(n, 10)
	}
	if u, err := strconv.ParseUint(txt, 10, 64); err == nil {
		return strconv.FormatUint(u, 10)
	}
	f, err := strconv.ParseFloat(txt, 64)
	if err != nil {
		return txt // 既不是整型也超出 float64 范围：退回原文（确定性由调用方输入决定）
	}
	if f == math.Trunc(f) {
		// `float64(1<<63)` 已经超出 int64 上界，必须用 `<` 而不是 `<=`（否则转换是未定义行为）。
		const maxInt64AsFloat = float64(1 << 63)
		if f >= -maxInt64AsFloat && f < maxInt64AsFloat {
			return strconv.FormatInt(int64(f), 10)
		}
		const maxUint64AsFloat = float64(1 << 64)
		if f >= 0 && f < maxUint64AsFloat {
			return strconv.FormatUint(uint64(f), 10)
		}
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
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
	// 数字：写侧与读侧**用同一个编码器产出文本**（`encoding/json`），再走同一个 `canonicalNumber`。
	//
	// 为什么要借 `json.Marshal` 而不是自己拼 `FormatFloat`：读回来的文本是 `encoding/json`
	// 给的（例如 `float64(7.0888e16)` → `70888282338495940`，而 `FormatFloat('g')` 是
	// `7.088828233849594e+16`）。两侧文本不同就会算出不同 tag —— 自己写的链自己验不过。
	// 借同一个编码器后，"写侧的值"与"读回的文本"天然一致，只剩跨类型归一化要做。
	if txt, ok, err := numberText(v); err != nil {
		return err
	} else if ok {
		e.str("n")
		e.str(canonicalNumber(txt))
		return nil
	}

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
