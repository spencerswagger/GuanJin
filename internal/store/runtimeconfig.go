package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// KeyFlowTTLSeconds 是阶梯认证流 TTL 的 runtime_config 键（D5 冻结）。
//
// **代码里不得出现 600 常量**：初值 600 由 `000015_create_flows` 迁移 seed 进
// `runtime_config`（`ON CONFLICT DO NOTHING`），运行时只从库里读；
// 键缺失 = 启动 fail-fast（没有"读不到就用默认值"的分支）。
const KeyFlowTTLSeconds = "flow.ttl_seconds"

// KeyChallengeTTLSeconds 是一次性挑战（`challenges.expires_at`）有效期的 runtime_config 键（D14①）。
//
// 同样**代码里不得出现 300 常量**：初值 300 由 `000018_seed_challenge_config` 迁移 seed，
// 运行时只从库里读；键缺失 = 启动 fail-fast。
//
// 注册窗口（`register_token` 的 `exp`）**复用本键**，不另开第三个键（D14①）：
// 挑战窗口与注册窗口是同一个安全参数，拆成两个键就会出现"码 5 分钟过期、令牌 30 分钟可用"的
// 自相矛盾组合，且没有任何裁定依据。
const KeyChallengeTTLSeconds = "challenge.ttl_seconds"

// KeyChallengeMaxAttempts 是单挑战允许的失败校验次数的 runtime_config 键（D14①）。
//
// 它**不是**秒数（300 / 5 的坑）：绝不能用 `RequiredIntSeconds` 读——那会把 5 解释成 5ns。
// 读法固定为 `RequiredPositiveInt`；`challenges.max_attempts` 列的 DB DEFAULT 已由
// `000018` 移除，所以本键是该列取值的**唯一来源**（漏传/缺键都必须显式失败，不许兜底 5）。
const KeyChallengeMaxAttempts = "challenge.max_attempts"

// 配置读取的错误（sentinel，便于启动期 fail-fast 分支明确）。
var (
	// ErrConfigKeyMissing 表示启动必需的 runtime_config 键不存在。
	ErrConfigKeyMissing = errors.New("store: required runtime_config key is missing")
	// ErrConfigTypeMismatch 表示键存在但 value_type 与期望不符（如 int 键写成 secret）。
	ErrConfigTypeMismatch = errors.New("store: runtime_config value_type mismatch")
	// ErrConfigValueInvalid 表示值本身不可解释（如不是整数、≤0）。
	ErrConfigValueInvalid = errors.New("store: runtime_config value invalid")
)

// ConfigReader 是 runtime_config 的**窄读取接口**（只读一个键）。
//
// 用接口而不是直接接 *gorm.DB：启动期校验要能在单测里用假实现跑（不需要真库），
// 而且调用方一眼看得出"这里只读、不写"。
type ConfigReader interface {
	// Lookup 按键读取配置；键不存在返回 ErrConfigKeyMissing。
	Lookup(ctx context.Context, key string) (*model.RuntimeConfig, error)
}

// RuntimeConfigRepo 是 `runtime_config` 的仓储（D5 的 flow.ttl_seconds 落点）。
type RuntimeConfigRepo struct {
	db *gorm.DB
}

// NewRuntimeConfigRepo 绑定连接。
func NewRuntimeConfigRepo(db *gorm.DB) *RuntimeConfigRepo { return &RuntimeConfigRepo{db: db} }

// Lookup 按键读取一行；不存在 → ErrConfigKeyMissing（**不是** 返回零值）。
func (r *RuntimeConfigRepo) Lookup(ctx context.Context, key string) (*model.RuntimeConfig, error) {
	if key == "" {
		return nil, fmt.Errorf("%w: empty key", ErrConfigKeyMissing)
	}
	var row model.RuntimeConfig
	err := r.db.WithContext(ctx).Where(`"key" = ?`, key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: %q", ErrConfigKeyMissing, key)
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &row, nil
}

// startupKeyKind 是启动期键的**解释方式**（不是校验方式）。
//
// 两类键的校验规则完全一样（`value_type=int` 且值 > 0），差别只在读出来之后怎么用：
// 秒数要乘成 `time.Duration`，次数就只是个整数。把"解释"单独建模，
// 是为了让 `challenge.max_attempts` 不可能被误交给 `RequiredIntSeconds`
// （那会把 5 读成 5ns——D14① 点名要避免的语义错）。
type startupKeyKind uint8

const (
	// kindSeconds 表示值是秒数，读出来转成 time.Duration。
	kindSeconds startupKeyKind = iota + 1
	// kindCount 表示值是次数/个数，**不得**转成时间。
	kindCount
)

// startupSpec 是「启动期必备键 → 解释方式」的唯一清单。
//
// 键名与解释方式写在一起，新增键只加一行：不会出现"加了键忘了进 StartupKeys()"
// 或"次数键被当成秒数读"这两类只有跑起来才会发现的错。
type startupSpec struct {
	key  string
	kind startupKeyKind
}

var startupSpecs = []startupSpec{
	{key: KeyFlowTTLSeconds, kind: kindSeconds},
	{key: KeyChallengeTTLSeconds, kind: kindSeconds},
	{key: KeyChallengeMaxAttempts, kind: kindCount},
}

// StartupKeys 返回启动期**必须存在**的 runtime_config 键（缺失即启动失败）。
//
// 三态口径（`decisions.md` **D5-₂**，归属 **S10** 的启动装配；本包只提供判据，不决定谁来调）：
//  1. **环境变量错**（主密钥缺失/非 base64/<32B 等）→ 启动**退出**并说明是哪一项；
//  2. **DB 不可达** → **不退出**：`/livez` 200、`/readyz` 503 `DB_UNAVAILABLE`、业务请求全拒
//     （fail-closed 指的是"拒服"，不是"进程起不来"——否则多副本下会连探针都没有）；
//  3. **DB 可达但缺键** → 启动**退出**，并打印缺失键名（本文件的 RequiredIntSeconds /
//     RequiredPositiveInt 负责，`ValidateStartup` 一次报全）。
//
// 所以 `LoadFlowTTL` / `ValidateStartup` 必须在"DB 已确认可达"之后才调，
// 不能拿它的错误去判 DB 是否可用（那会把两种故障混成一种）。
//
// 键清单：`flow.ttl_seconds`、`challenge.ttl_seconds`、`challenge.max_attempts`（D14①）。
func StartupKeys() []string {
	out := make([]string, 0, len(startupSpecs))
	for _, spec := range startupSpecs {
		out = append(out, spec.key)
	}
	return out
}

// requiredPositiveInt 是所有整数配置的**唯一取值口径**：存在 + `value_type='int'` + 值 > 0。
//
// 三重校验缺一条，配置错误就会藏到运行期：
//  1. 键必须存在（ErrConfigKeyMissing）；
//  2. value_type 必须是 `int`（ErrConfigTypeMismatch）；
//  3. 值必须是 **> 0 的整数**（ErrConfigValueInvalid）——0 或负数让"TTL/次数"语义失效。
//
// 不做 `if key == KeyFlowTTLSeconds { return 600 }` 之类的兜底：那正是 D5/D14 禁止的
// 隐性默认值（数据库默认值 + 代码兜底 = 两套来源，判据"改键值必须跟着变"就失效）。
func requiredPositiveInt(ctx context.Context, reader ConfigReader, key string) (int, error) {
	if reader == nil {
		return 0, errors.New("store: nil config reader")
	}
	row, err := reader.Lookup(ctx, key)
	if err != nil {
		return 0, err
	}
	if row.ValueType != "int" {
		return 0, fmt.Errorf("%w: %q is %q, want \"int\"", ErrConfigTypeMismatch, key, row.ValueType)
	}
	value, err := row.Value.AsInt()
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrConfigValueInvalid, key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%w: %q = %d, must be > 0", ErrConfigValueInvalid, key, value)
	}
	return value, nil
}

// RequiredIntSeconds 读取一个整数秒配置并转成 time.Duration。
//
// 只用于**秒数**类键（`flow.ttl_seconds` / `challenge.ttl_seconds`）。
// 次数类键（`challenge.max_attempts`）必须走 `RequiredPositiveInt`：本函数会把 5 变成 5ns。
func RequiredIntSeconds(ctx context.Context, reader ConfigReader, key string) (time.Duration, error) {
	value, err := requiredPositiveInt(ctx, reader, key)
	if err != nil {
		return 0, err
	}
	return time.Duration(value) * time.Second, nil
}

// RequiredPositiveInt 读取一个正整数配置（次数/个数，如 `challenge.max_attempts`）。
//
// 与 `RequiredIntSeconds` 是**两个入口而不是一个带开关的函数**：语义不同的东西
// 用不同签名，调用点就写不出"把次数当秒数"这种错。
func RequiredPositiveInt(ctx context.Context, reader ConfigReader, key string) (int, error) {
	return requiredPositiveInt(ctx, reader, key)
}

// LoadFlowTTL 是 `flow.ttl_seconds` 的唯一读取口径（S3 的启动期 fail-fast 落点）。
//
// 调用时机：进程启动装配阶段（早于接受请求）。返回错误 → main 打日志并非 0 退出。
func LoadFlowTTL(ctx context.Context, reader ConfigReader) (time.Duration, error) {
	return RequiredIntSeconds(ctx, reader, KeyFlowTTLSeconds)
}

// LoadChallengeTTL 是 `challenge.ttl_seconds` 的唯一读取口径（D14①）。
//
// 一次性挑战的 `expires_at` 与注册窗口（`register_token` 的 `exp`）都取它，
// 所以调用方**只读一次**、两处共用同一个值，避免窗口不一致。
func LoadChallengeTTL(ctx context.Context, reader ConfigReader) (time.Duration, error) {
	return RequiredIntSeconds(ctx, reader, KeyChallengeTTLSeconds)
}

// LoadChallengeMaxAttempts 是 `challenge.max_attempts` 的唯一读取口径（D14①）。
//
// 返回值直接落 `challenges.max_attempts`（`Attempts` 初值 0，`attempts_left = max - attempts`）。
// 该列已无 DB DEFAULT，本函数就是唯一来源。
func LoadChallengeMaxAttempts(ctx context.Context, reader ConfigReader) (int, error) {
	return RequiredPositiveInt(ctx, reader, KeyChallengeMaxAttempts)
}

// StartupConfig 是启动期从 `runtime_config` 读到的全部参数（已解释、可直接用）。
type StartupConfig struct {
	// FlowTTL = `flow.ttl_seconds`（阶梯中间态过期）。
	FlowTTL time.Duration
	// ChallengeTTL = `challenge.ttl_seconds`（一次性挑战 + 注册窗口）。
	ChallengeTTL time.Duration
	// ChallengeMaxAttempts = `challenge.max_attempts`（单挑战失败次数上限）。
	ChallengeMaxAttempts int
}

// ValidateStartup 校验全部启动期键存在且可用，返回已解释的值。
//
// 一次把**所有**缺失键都报出来，而不是遇到第一个就退出：启动期反复重启找问题是纯浪费。
// 键清单来自 `startupSpecs`（与 `StartupKeys()` 同一份），所以"哪类键走哪种解释"
// 是数据驱动的——加键不用改本函数。
func ValidateStartup(ctx context.Context, reader ConfigReader) (StartupConfig, error) {
	var out StartupConfig
	if reader == nil {
		return out, errors.New("store: nil config reader")
	}
	values := make(map[string]int, len(startupSpecs))
	var failures []error
	for _, spec := range startupSpecs {
		value, err := requiredPositiveInt(ctx, reader, spec.key)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		values[spec.key] = value
	}
	if len(failures) > 0 {
		return StartupConfig{}, fmt.Errorf("store: runtime_config startup validation failed: %w",
			errors.Join(failures...))
	}
	// 解释（秒 → Duration / 次数原样）只在这里发生一次；键清单仍在 startupSpecs 里。
	out.FlowTTL = time.Duration(values[KeyFlowTTLSeconds]) * time.Second
	out.ChallengeTTL = time.Duration(values[KeyChallengeTTLSeconds]) * time.Second
	out.ChallengeMaxAttempts = values[KeyChallengeMaxAttempts]
	return out, nil
}
