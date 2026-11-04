package credential

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

const (
	// TOTPAlgo 是动态口令信封的算法名（数据模型 §3.3 的 `totp` 信封）。
	TOTPAlgo = "TOTP"
	// TOTPHashSM3 是 HMAC 原语：**SM3**（全链路国密，索引 §2；禁 SHA 系）。
	TOTPHashSM3 = "SM3"
	// TOTPDefaultDigits / TOTPDefaultPeriodSeconds 是 RFC 6238 的默认形态，
	// 与主流 authenticator 兼容。注意：这是**默认值**，落库时由信封显式记录，
	// 校验时必须用信封里的值（不许在读路径上再取一次默认）。
	TOTPDefaultDigits       = 6
	TOTPDefaultPeriodSecond = 30
	// MinTOTPSecretBytes 是共享密钥最小长度（RFC 4226 建议 160bit）。
	MinTOTPSecretBytes = 20
	// MaxTOTPSkewSteps 是允许的时间窗偏移上限（步数）。
	//
	// 上限本身是安全参数：窗口每放宽一步，可被猜中的码就多一个，
	// 而攻击者无需知道时间——所以"允许 ±5 步（±150s）"就是硬上限，不许配置更大。
	MaxTOTPSkewSteps = 5
)

// TOTPEnvelope 是动态口令凭据在 `authenticators.credential_data` 里的形态。
//
// `SecretEnc` 是 **SM4 密文（base64）**，密钥来自 `crypto.MasterKey` 的派生（D4-B）。
// 本包不持有主密钥：加解密都由调用方传入 `crypto.SM4Key`。
type TOTPEnvelope struct {
	Algo      string `json:"algo"`
	Hash      string `json:"hash"`
	Digits    int    `json:"digits"`
	Period    int    `json:"period"`
	SecretEnc string `json:"secret_enc"`
	BoundAt   string `json:"bound_at,omitempty"`
	// LastUsedStep 是**已消费到的时间步**，用于重放防护（同一窗口内二次使用即拒绝）。
	//
	// 0 表示"从未用过"：Unix 时间步 0 是 1970-01-01 的窗口，永不可能成为真实挑战的窗口，
	// 所以它可以安全地当哨兵值（这是**显式约定**，不是隐式默认）。
	LastUsedStep int64 `json:"last_step,omitempty"`
}

// NewTOTPEnvelope 生成共享密钥并用调用方给的 SM4 键封起来。
//
// 第二个返回值是**明文密钥**，只用于"绑定那一刻"渲染二维码/手抄；
// 调用方拿到后必须立即丢弃，不得落日志、不得进响应体之外的任何地方。
func NewTOTPEnvelope(key crypto.SM4Key, random io.Reader, now time.Time) (TOTPEnvelope, []byte, error) {
	if key.IsZero() {
		return TOTPEnvelope{}, nil, ErrMissingKey
	}
	if random == nil {
		random = rand.Reader
	}
	secret := make([]byte, MinTOTPSecretBytes)
	if _, err := io.ReadFull(random, secret); err != nil {
		return TOTPEnvelope{}, nil, wrapf(ErrInvalidSecret, "random: %v", err)
	}
	sealed, err := crypto.EncryptSM4(key, secret)
	if err != nil {
		return TOTPEnvelope{}, nil, wrapf(ErrMalformedEnvelope, "seal secret: %v", err)
	}
	return TOTPEnvelope{
		Algo:      TOTPAlgo,
		Hash:      TOTPHashSM3,
		Digits:    TOTPDefaultDigits,
		Period:    TOTPDefaultPeriodSecond,
		SecretEnc: base64.StdEncoding.EncodeToString(sealed),
		BoundAt:   now.UTC().Format(time.RFC3339),
	}, secret, nil
}

// Validate 检查信封结构（算法名、位数、周期、密文存在）。
func (e TOTPEnvelope) Validate() error {
	if e.Algo != TOTPAlgo {
		return wrapf(ErrUnknownAlgo, "%q (required: %s)", e.Algo, TOTPAlgo)
	}
	if e.Hash != TOTPHashSM3 {
		// 这一条是合规硬约束：SHA 系 HMAC 一律不许（索引 §2「禁止与 SHA 系列混用」）。
		return wrapf(ErrUnknownAlgo, "hash %q (required: %s)", e.Hash, TOTPHashSM3)
	}
	if e.Digits < 6 || e.Digits > 8 {
		return wrapf(ErrInvalidOptions, "digits %d out of range 6..8", e.Digits)
	}
	if e.Period < 10 || e.Period > 300 {
		return wrapf(ErrInvalidOptions, "period %d out of range 10..300 seconds", e.Period)
	}
	if e.SecretEnc == "" {
		return wrapf(ErrMalformedEnvelope, "secret_enc is empty")
	}
	if _, err := base64.StdEncoding.DecodeString(e.SecretEnc); err != nil {
		return wrapf(ErrMalformedEnvelope, "secret_enc is not base64: %v", err)
	}
	if e.BoundAt != "" {
		if _, err := time.Parse(time.RFC3339, e.BoundAt); err != nil {
			return wrapf(ErrMalformedEnvelope, "bound_at is not RFC3339: %v", err)
		}
	}
	return nil
}

// Open 解出共享密钥明文（只在校验/重新绑定时调用）。
func (e TOTPEnvelope) Open(key crypto.SM4Key) ([]byte, error) {
	if key.IsZero() {
		return nil, ErrMissingKey
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(e.SecretEnc)
	if err != nil {
		return nil, wrapf(ErrMalformedEnvelope, "secret_enc is not base64: %v", err)
	}
	secret, err := crypto.DecryptSM4(key, sealed)
	if err != nil {
		// **D15④**：键是给了的，解不开只可能是"版本不符 / MAC 不过 / 结构损坏" ——
		// 属数据缺陷（500 `INTERNAL_ERROR`），不是"密钥没装配"（503 `KEY_UNAVAILABLE`）。
		// 两种都**绝不**退化成"跳过校验"。
		//
		// **双 `%w`**：底层 `crypto` 的哨兵必须留在错误链里 ——
		// `ErrCiphertextVersion`（存量 v1 未重加密 → 运维动作：重灌/一次性重加密）与
		// `ErrIntegrityMismatch`（密文被改 → 安全事件）在日志里必须是可区分的两种线索，
		// 只把文本拼进去（`%v`）会让两者都只剩一个 500。
		return nil, fmt.Errorf("%w: open secret: %w", ErrIntegrityMismatch, err)
	}
	if len(secret) < MinTOTPSecretBytes {
		return nil, wrapf(ErrInvalidSecret, "decrypted %d bytes < %d", len(secret), MinTOTPSecretBytes)
	}
	return secret, nil
}

// CodeOptions 是生成码所需的参数（来自信封，不许在调用处再取默认）。
type CodeOptions struct {
	Digits int
	Period time.Duration
}

func (o CodeOptions) validate() error {
	if o.Digits < 6 || o.Digits > 8 {
		return wrapf(ErrInvalidOptions, "digits %d out of range 6..8", o.Digits)
	}
	if o.Period < 10*time.Second || o.Period > 300*time.Second {
		return wrapf(ErrInvalidOptions, "period %s out of range 10s..300s", o.Period)
	}
	return nil
}

// TOTPOptions 是校验参数：额外带时间窗偏移（步数）。
type TOTPOptions struct {
	Digits int
	Period time.Duration
	// Skew 允许的时间步偏移（对称窗口，0..5）。
	Skew int
}

func (o TOTPOptions) validate() error {
	if err := (CodeOptions{Digits: o.Digits, Period: o.Period}).validate(); err != nil {
		return err
	}
	if o.Skew < 0 || o.Skew > MaxTOTPSkewSteps {
		return wrapf(ErrInvalidOptions, "skew %d out of range 0..%d", o.Skew, MaxTOTPSkewSteps)
	}
	return nil
}

// TOTPResult 是一次校验命中的结果。
//
// `MatchedStep` 要交给 `ReplayState.Accept` 做重放判定——**校验通过与防重放是两步**，
// 不能合成一步：同一个码在窗口内可能被并发提交两次，两次都能"校验通过"，
// 只有把"已消费到哪一步"落库（CAS）才能真正挡住第二次。
type TOTPResult struct {
	MatchedStep int64
	Drift       int
}

// GenerateTOTP 计算 `at` 所在时间步的动态口令（6~8 位十进制）。
func GenerateTOTP(secret []byte, at time.Time, opt CodeOptions) (string, error) {
	if err := opt.validate(); err != nil {
		return "", err
	}
	if len(secret) < MinTOTPSecretBytes {
		return "", wrapf(ErrInvalidSecret, "%d bytes < %d", len(secret), MinTOTPSecretBytes)
	}
	return hotp(secret, stepAt(at, opt.Period), opt.Digits)
}

// VerifyTOTP 在 `±Skew` 个时间步内校验动态口令（常量时间比较）。
//
// 返回 `ErrInvalidCredential`（值不对）或 `ErrMalformedCode`（形态不对），
// 以及命中时的 `MatchedStep/Drift`。
func VerifyTOTP(secret []byte, code string, at time.Time, opt TOTPOptions) (TOTPResult, error) {
	if err := opt.validate(); err != nil {
		return TOTPResult{}, err
	}
	if len(secret) < MinTOTPSecretBytes {
		return TOTPResult{}, wrapf(ErrInvalidSecret, "%d bytes < %d", len(secret), MinTOTPSecretBytes)
	}
	if len(code) != opt.Digits {
		return TOTPResult{}, wrapf(ErrMalformedCode, "length %d != %d", len(code), opt.Digits)
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return TOTPResult{}, wrapf(ErrMalformedCode, "non-digit character")
		}
	}

	cur := stepAt(at, opt.Period)
	// 先试当前步，再按 ±1、±2 … 展开：命中概率最高的一步排在最前，
	// 且 `Drift` 的语义（实际步 - 当前步）对调用方是稳定的。
	for _, drift := range driftOrder(opt.Skew) {
		step := cur + int64(drift)
		if step < 0 {
			continue
		}
		want, err := hotp(secret, step, opt.Digits)
		if err != nil {
			return TOTPResult{}, err
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return TOTPResult{MatchedStep: step, Drift: drift}, nil
		}
	}
	return TOTPResult{}, ErrInvalidCredential
}

// ReplayState 是"这条凭据已消费到哪个时间步"。
type ReplayState struct {
	// LastUsedStep 见 `TOTPEnvelope.LastUsedStep`（0 = 从未使用）。
	LastUsedStep int64
}

// Accept 判定本次命中是否可以接受，并返回推进后的状态。
//
// 规则只有一条：**命中的步必须严格大于已消费的步**。
//   - 同一步再来一次 = 同窗内重放 → `ErrTemporalCodeReused`；
//   - 更早的步 = 陈旧码（时钟倒退或抓包重放）→ 同样拒绝。
//
// ⚠️ 落库必须与校验**同一次原子写**，否则并发双花还是挡不住。契约（S4b 照此实现）：
//
//	UPDATE authenticators
//	   SET credential_data = jsonb_set(credential_data, '{last_step}', to_jsonb($2::bigint))
//	 WHERE id = $1
//	   AND COALESCE((credential_data->>'last_step')::bigint, 0) < $2
//
// 影响行数为 0 即表示"已被别人抢先消费" → 按 `TEMPORAL_CODE_REUSED` 处理。
func (s ReplayState) Accept(matchedStep int64) (ReplayState, error) {
	if matchedStep <= s.LastUsedStep {
		return s, ErrTemporalCodeReused
	}
	return ReplayState{LastUsedStep: matchedStep}, nil
}

// stepAt 返回 t 所在的时间步。
func stepAt(t time.Time, period time.Duration) int64 {
	return t.UTC().Unix() / int64(period/time.Second)
}

// driftOrder 生成 `0, -1, +1, -2, +2 …`（先当前、再对称展开）。
func driftOrder(skew int) []int {
	out := make([]int, 0, 2*skew+1)
	out = append(out, 0)
	for d := 1; d <= skew; d++ {
		out = append(out, -d, d)
	}
	return out
}

// hotp 是 RFC 4226 的 HOTP（PRF 固定 HMAC-SM3），返回零填充的十进制码。
//
// 截断按 RFC 4226 §5.3 的 dynamic truncation；HMAC 键是**用户侧共享密钥**，
// 所以走 `crypto.HMACSM3WithUserSecret`（不是平台派生键那条路）。
func hotp(secret []byte, counter int64, digits int) (string, error) {
	if len(secret) == 0 {
		return "", wrapf(ErrInvalidSecret, "empty secret")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	sum := crypto.HMACSM3WithUserSecret(secret, msg[:])
	if len(sum) != crypto.SM3Size {
		return "", wrapf(ErrInvalidSecret, "hmac output %d bytes", len(sum))
	}
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	value := bin % mod
	s := strconv.FormatUint(uint64(value), 10)
	for len(s) < digits {
		s = "0" + s
	}
	return s, nil
}

// String 固定脱敏（信封里有密文，别把密文抄进日志）。
func (e TOTPEnvelope) String() string {
	return fmt.Sprintf("credential.TOTPEnvelope{algo=%s, hash=%s, digits=%d, period=%d}", e.Algo, e.Hash, e.Digits, e.Period)
}

// GoString 让 %#v 也脱敏。
func (e TOTPEnvelope) GoString() string { return e.String() }

// Marshal 序列化成落库用的 JSON（`credential_data` 列）。
func (e TOTPEnvelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, wrapf(ErrMalformedEnvelope, "marshal: %v", err)
	}
	return raw, nil
}

// ParseTOTPEnvelope 从 JSON 还原信封（解析即校验）。
func ParseTOTPEnvelope(raw []byte) (TOTPEnvelope, error) {
	var e TOTPEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return TOTPEnvelope{}, wrapf(ErrMalformedEnvelope, "unmarshal: %v", err)
	}
	if err := e.Validate(); err != nil {
		return TOTPEnvelope{}, err
	}
	return e, nil
}
