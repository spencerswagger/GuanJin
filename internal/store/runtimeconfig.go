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

// StartupKeys 返回启动期**必须存在**的 runtime_config 键（缺失即启动失败）。
func StartupKeys() []string {
	return []string{KeyFlowTTLSeconds}
}

// RequiredIntSeconds 读取一个整数秒配置并转成 time.Duration。
//
// 三重校验，缺一条就会把配置错误藏到运行期：
//  1. 键必须存在（ErrConfigKeyMissing）；
//  2. value_type 必须是 `int`（ErrConfigTypeMismatch）；
//  3. 值必须是 **> 0 的整数**（ErrConfigValueInvalid）——0 或负数会让 TTL 语义失效。
//
// 回调里不做 `if key == KeyFlowTTLSeconds { return 600 }` 之类的兜底：
// 那正是 D5 明令禁止的隐性默认值。
func RequiredIntSeconds(ctx context.Context, reader ConfigReader, key string) (time.Duration, error) {
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
	seconds, err := row.Value.AsInt()
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrConfigValueInvalid, key, err)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("%w: %q = %d, must be > 0", ErrConfigValueInvalid, key, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// LoadFlowTTL 是 `flow.ttl_seconds` 的唯一读取口径（S3 的启动期 fail-fast 落点）。
//
// 调用时机：进程启动装配阶段（早于接受请求）。返回错误 → main 打日志并非 0 退出。
func LoadFlowTTL(ctx context.Context, reader ConfigReader) (time.Duration, error) {
	return RequiredIntSeconds(ctx, reader, KeyFlowTTLSeconds)
}

// ValidateStartup 校验全部启动期键存在且可用，返回读到的值（键 → 时长）。
//
// 一次把**所有**缺失键都报出来，而不是遇到第一个就退出：启动期反复重启找问题是纯浪费。
func ValidateStartup(ctx context.Context, reader ConfigReader) (map[string]time.Duration, error) {
	if reader == nil {
		return nil, errors.New("store: nil config reader")
	}
	out := make(map[string]time.Duration, len(StartupKeys()))
	var failures []error
	for _, key := range StartupKeys() {
		d, err := RequiredIntSeconds(ctx, reader, key)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		out[key] = d
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("store: runtime_config startup validation failed: %w",
			errors.Join(failures...))
	}
	return out, nil
}
