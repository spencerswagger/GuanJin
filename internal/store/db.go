package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 连接池的默认值来自技术栈 §7.2 的 envDefault（50 / 10 / 30m）。
//
// 它们是**显式登记的默认值**（文档里有出处），不是实现随手拍的数；
// 调用方（config）一旦接入这三个环境变量，就以配置值为准。
const (
	DefaultMaxOpenConns    = 50
	DefaultMaxIdleConns    = 10
	DefaultConnMaxLifetime = 30 * time.Minute
)

// Options 是打开数据库连接的显式参数。
type Options struct {
	// DSN 必填（fail-closed：不猜默认 DSN，猜一个只会换来 password authentication failed）。
	DSN string
	// MaxOpenConns / MaxIdleConns / ConnMaxLifetime 为 0 时取上面登记的默认值。
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	// Logger 为空时用 GORM 的 silent logger（业务错误由 MapError 统一成 §5.4 码，
	// 不需要 GORM 自己再打一遍 SQL 日志；要排查时显式传 Info 级别的 logger）。
	Logger logger.Interface
}

// Open 建立 GORM 连接（postgres 驱动 = pgx）。
//
// 三件刻意不做的隐性动作：
//  1. **不做任何 schema 变更**：项目硬约束，`AutoMigrate` 一次都不许调（CI 有门禁扫描）；
//  2. **不吞约束名**：故意关掉 GORM 的 `TranslateError`（它会把 23505 拍平成
//     `gorm.ErrDuplicatedKey`，约束名就丢了，双键映射也就废了）；
//  3. **不自动 ping**：连接是否可用由调用方显式 `Ping` 判定（启动期 fail-fast 要看得见）。
func Open(ctx context.Context, opt Options) (*gorm.DB, error) {
	if strings.TrimSpace(opt.DSN) == "" {
		return nil, errors.New("store: database DSN is required (fail-closed)")
	}

	cfg := &gorm.Config{
		// 关掉"自动翻译错误"：保留原始的 pgconn 约束名，MapError 才有第二把钥匙。
		TranslateError: false,
		// 由调用方显式 Ping（见函数注释 3）。
		DisableAutomaticPing: true,
		// 单条 Create 也要进事务：与多语句写行为一致，避免"单条写成功、后续失败"的半截状态。
		SkipDefaultTransaction: false,
		Logger:                 opt.Logger,
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.Default.LogMode(logger.Silent)
	}

	db, err := gorm.Open(postgres.Open(opt.DSN), cfg)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store: underlying sql.DB: %w", err)
	}
	open := opt.MaxOpenConns
	if open <= 0 {
		open = DefaultMaxOpenConns
	}
	idle := opt.MaxIdleConns
	if idle <= 0 {
		idle = DefaultMaxIdleConns
	}
	lifetime := opt.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = DefaultConnMaxLifetime
	}
	sqlDB.SetMaxOpenConns(open)
	sqlDB.SetMaxIdleConns(idle)
	sqlDB.SetConnMaxLifetime(lifetime)

	return db.WithContext(ctx), nil
}

// Ping 探测数据库可用性（启动期 fail-fast、`/readyz` 复用）。
func Ping(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("store: nil db")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("store: underlying sql.DB: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Close 关闭底层连接池（进程退出时调用）。
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Tx 在一个事务里执行 fn：fn 返回错误即回滚，panic 也回滚（不吞 panic）。
//
// 锁序口径（D5 I-F8）：全站加锁顺序 **sessions → flows**；
// 本函数不替调用方排序——排序是编排层（Core）的责任，这里只保证"一个 fn 一个事务"。
func Tx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("store: nil db")
	}
	if fn == nil {
		return errors.New("store: nil tx func")
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
	if err != nil {
		return MapError(err)
	}
	return nil
}

// DBNow 读**库时间**（`now()`）。
//
// 为什么不用应用时钟：一次性挑战的过期、注册窗口、流 TTL 都用库时间判定（D5/D14），
// 多副本时钟漂移会让"同一个瞬间"在两台机器上得出不同结论。凡是"权威时刻"都必须走这里。
func DBNow(ctx context.Context, db *gorm.DB) (time.Time, error) {
	if db == nil {
		return time.Time{}, errors.New("store: nil db")
	}
	var now time.Time
	if err := db.WithContext(ctx).Raw(`SELECT now()`).Scan(&now).Error; err != nil {
		return time.Time{}, MapError(err)
	}
	return now.UTC(), nil
}
