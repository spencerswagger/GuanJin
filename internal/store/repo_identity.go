package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**账号聚合**（users / identifiers / authenticators）的仓储。
//
// 为什么三张表放一个文件：它们的不变式是**跨表**的（I1「至少一个已验证标识符」、
// I3「至少一个可用认证能力」= holding 认证器 ∪ 已验证的认证型标识符），
// 拆到三个文件只会让人以为可以各改各的。事务边界仍由调用方（`internal/identity`）持有。
//
// **锁序（D5 I-F8 / 全站 `sessions → flows`）**：本聚合内一律 `users → identifiers/authenticators`。
// 变更某用户的标识符/凭据前先锁**用户行**（`LockUser`），这样"并发解绑最后两个标识符"
// 不会两个事务都看到 2 条、各自删掉一条——那正是 I1/I2 最典型的绕过方式。

var (
	// ErrUserNotFound 表示用户不存在（`USER_NOT_FOUND`，E4/404）。
	ErrUserNotFound = errors.New("store: user not found")
	// ErrIdentifierNotFound 表示标识符不存在（`IDENTIFIER_NOT_FOUND`，E4/404）。
	ErrIdentifierNotFound = errors.New("store: identifier not found")
	// ErrAuthenticatorNotFound 表示认证器不存在。
	ErrAuthenticatorNotFound = errors.New("store: authenticator not found")
)

// UserRepo 是 `users` 的仓储。
type UserRepo struct{ db *gorm.DB }

// NewUserRepo 绑定连接（db 可为连接或事务句柄）。
func NewUserRepo(db *gorm.DB) *UserRepo { return &UserRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *UserRepo) WithTx(tx *gorm.DB) *UserRepo { return &UserRepo{db: tx} }

// Create 插入用户；用户名重复由 `users_username_lower_uidx` 拦，经 MapError 映射成
// `IDENTIFIER_ALREADY_BOUND`（CH-7②：用户名也是标识符）。
func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	if u == nil {
		return errors.New("store: nil user")
	}
	err := r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(u).Error
	return MapError(err)
}

// GetByID 读用户（不加锁）。
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	if id == uuid.Nil {
		return nil, ErrUserNotFound
	}
	var u model.User
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &u, nil
}

// LockUser 在调用方事务里对用户行 `SELECT … FOR UPDATE`（账号聚合的唯一加锁点）。
//
// 为什么锁用户行而不是"锁全部标识符行"：标识符是**集合**，锁集合挡不住"并发新增"；
// 锁用户行等于给"这个账号的凭据/标识符变更"上一把单写者锁，语义简单且不会死锁
// （锁序恒为 users → identifiers/authenticators）。
func (r *UserRepo) LockUser(ctx context.Context, tx *gorm.DB, id uuid.UUID) (*model.User, error) {
	if tx == nil {
		return nil, ErrNilTx
	}
	if id == uuid.Nil {
		return nil, ErrUserNotFound
	}
	var u model.User
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &u, nil
}

// IdentifierRepo 是 `identifiers` 的仓储。
type IdentifierRepo struct{ db *gorm.DB }

// NewIdentifierRepo 绑定连接。
func NewIdentifierRepo(db *gorm.DB) *IdentifierRepo { return &IdentifierRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *IdentifierRepo) WithTx(tx *gorm.DB) *IdentifierRepo { return &IdentifierRepo{db: tx} }

// Create 插入标识符；(type,value) 或主标识符唯一冲突由 MapError 映射
// （CH-7① `IDENTIFIER_ALREADY_BOUND`；`identifiers_one_primary_per_user` 按 D3-B 不映射 → 500，属缺陷）。
func (r *IdentifierRepo) Create(ctx context.Context, i *model.Identifier) error {
	if i == nil {
		return errors.New("store: nil identifier")
	}
	err := r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(i).Error
	return MapError(err)
}

// GetByID 读标识符（不加锁）。
func (r *IdentifierRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Identifier, error) {
	if id == uuid.Nil {
		return nil, ErrIdentifierNotFound
	}
	var i model.Identifier
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&i).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIdentifierNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &i, nil
}

// FindByBlindIndex 按 (type, 盲索引) 等值查询——注册查重与登录查人的唯一入口。
//
// 注意参数是**盲索引**而不是明文：加密与规范化发生在 `internal/identifier`，
// 仓储层拿不到明文，也就不会有人顺手在 SQL 里 `= '138...'`。
func (r *IdentifierRepo) FindByBlindIndex(ctx context.Context, itype string, blind []byte) (*model.Identifier, error) {
	if len(blind) == 0 {
		return nil, ErrIdentifierNotFound
	}
	var i model.Identifier
	err := r.db.WithContext(ctx).Where("type = ? AND value = ?", itype, blind).Take(&i).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIdentifierNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &i, nil
}

// ListByUser 列出某用户的全部标识符（按创建时间升序，输出稳定）。
func (r *IdentifierRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Identifier, error) {
	var out []model.Identifier
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at ASC, id ASC").Find(&out).Error
	if err != nil {
		return nil, MapError(err)
	}
	return out, nil
}

// CountByUser 统计该用户的标识符总数（I2："不能解绑最后一个"）。
func (r *IdentifierRepo) CountByUser(ctx context.Context, tx *gorm.DB, userID uuid.UUID) (int64, error) {
	return r.count(ctx, tx, userID, "user_id = ?", userID)
}

// CountVerified 统计已验证标识符数（I1："至少一个已验证"）。
func (r *IdentifierRepo) CountVerified(ctx context.Context, tx *gorm.DB, userID uuid.UUID) (int64, error) {
	return r.count(ctx, tx, userID, "user_id = ? AND verified AND status = 'active'", userID)
}

// CountVerifiedAuthable 统计**认证型**已验证标识符数（I3 并集语义里的"信道能力"一半：
// 只有 phone / email 能当登录方式，username 不能）。
//
// 为什么把类型写死在 SQL 里而不是传参：这是"哪些标识符类型具备认证能力"的**唯一权威**，
// 传参会让调用点各自决定（同一个账号在不同入口算出的"可用能力"不同）。
func (r *IdentifierRepo) CountVerifiedAuthable(ctx context.Context, tx *gorm.DB, userID uuid.UUID) (int64, error) {
	db := r.pick(tx)
	var n int64
	err := db.WithContext(ctx).Model(&model.Identifier{}).
		Where("user_id = ? AND verified AND status = 'active' AND type IN ('phone','email')", userID).
		Count(&n).Error
	if err != nil {
		return 0, MapError(err)
	}
	return n, nil
}

// CountPrimary 统计主标识符数（I6 的前置读数）。
func (r *IdentifierRepo) CountPrimary(ctx context.Context, tx *gorm.DB, userID uuid.UUID) (int64, error) {
	return r.count(ctx, tx, userID, "user_id = ? AND is_primary", userID)
}

func (r *IdentifierRepo) count(ctx context.Context, tx *gorm.DB, userID uuid.UUID, where string, args ...any) (int64, error) {
	var n int64
	err := r.pick(tx).WithContext(ctx).Model(&model.Identifier{}).Where(where, args...).Count(&n).Error
	if err != nil {
		return 0, MapError(err)
	}
	return n, nil
}

func (r *IdentifierRepo) pick(tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx
	}
	return r.db
}

// MarkVerified 把标识符置为已验证（写入 trust_level 与验证方式）。
//
// 用**条件更新**（`verified = false`）而不是无条件写：重复验证同一标识符时，
// 第二次应当拿到 0 行（调用方据此判"已经验证过了"），而不是把 trust_level 越冲越高。
func (r *IdentifierRepo) MarkVerified(ctx context.Context, id uuid.UUID, method string, trust int16, at time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.Identifier{}).
		Where("id = ? AND verified = false", id).
		Updates(map[string]any{
			"verified":      true,
			"verified_at":   at,
			"verify_method": method,
			"trust_level":   trust,
		})
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// SetStatus 置标识符状态（解绑 = `disabled`，**不物理删**：审计与唯一约束都要留痕）。
//
// 为什么不 DELETE：`(type,value)` 的唯一索引一旦行被删，同一个手机号可以立刻被另一个
// 账号抢注——"换绑冷却期"就形同虚设（`COOLDOWN_ACTIVE` 将无从判定）。
func (r *IdentifierRepo) SetStatus(ctx context.Context, tx *gorm.DB, id uuid.UUID, status string) (int64, error) {
	if status != "active" && status != "disabled" {
		return 0, errors.New("store: identifier status must be active or disabled")
	}
	res := r.pick(tx).WithContext(ctx).Model(&model.Identifier{}).
		Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// AuthenticatorRepo 是 `authenticators` 的仓储。
type AuthenticatorRepo struct{ db *gorm.DB }

// NewAuthenticatorRepo 绑定连接。
func NewAuthenticatorRepo(db *gorm.DB) *AuthenticatorRepo { return &AuthenticatorRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *AuthenticatorRepo) WithTx(tx *gorm.DB) *AuthenticatorRepo {
	return &AuthenticatorRepo{db: tx}
}

// Create 插入认证器。
func (r *AuthenticatorRepo) Create(ctx context.Context, a *model.Authenticator) error {
	if a == nil {
		return errors.New("store: nil authenticator")
	}
	err := r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(a).Error
	return MapError(err)
}

// GetByID 读认证器（不加锁）。
func (r *AuthenticatorRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Authenticator, error) {
	if id == uuid.Nil {
		return nil, ErrAuthenticatorNotFound
	}
	var a model.Authenticator
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAuthenticatorNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &a, nil
}

// ListActiveByUserType 列出某用户某类型下**可用**的认证器（登录校验与改密用）。
//
// "可用"= active + 未锁定 + 未过期；锁定以 `locked_until` 为准（它是 Redis 权威计数的镜像）。
func (r *AuthenticatorRepo) ListActiveByUserType(ctx context.Context, userID uuid.UUID, atype string, now time.Time) ([]model.Authenticator, error) {
	var out []model.Authenticator
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND type = ? AND status = 'active'", userID, atype).
		Where("(locked_until IS NULL OR locked_until < ?)", now).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Order("created_at DESC, id DESC").Find(&out).Error
	if err != nil {
		return nil, MapError(err)
	}
	return out, nil
}

// CountUsableHolding 统计"可用的持有型认证器"数（I3 并集语义里的另一半）。
//
// 注意这不含信道能力：I3 的**可用性判定**必须用 `CountVerifiedAuthable + CountUsableHolding`，
// 见 `internal/identifier` 的 `UsableAuthMethods`。分开统计是为了让两边各自的定义
// 只有一个出处，而不是在不同调用点各写一遍 SQL。
func (r *AuthenticatorRepo) CountUsableHolding(ctx context.Context, tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, error) {
	db := r.db
	if tx != nil {
		db = tx
	}
	var n int64
	err := db.WithContext(ctx).Model(&model.Authenticator{}).
		Where("user_id = ? AND status = 'active'", userID).
		Where("(locked_until IS NULL OR locked_until < ?)", now).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Count(&n).Error
	if err != nil {
		return 0, MapError(err)
	}
	return n, nil
}

// UpdateCredentialData 覆盖凭据信封（改密 / 重绑 TOTP / 恢复码重发）。
func (r *AuthenticatorRepo) UpdateCredentialData(ctx context.Context, tx *gorm.DB, id uuid.UUID, data model.JSONValue) (int64, error) {
	if len(data) == 0 {
		return 0, errors.New("store: empty credential_data (refusing to wipe a credential)")
	}
	db := r.db
	if tx != nil {
		db = tx
	}
	res := db.WithContext(ctx).Model(&model.Authenticator{}).
		Where("id = ?", id).Update("credential_data", data)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// SetStatus 置认证器状态（撤销 = `revoked`，停用 = `disabled`）。
func (r *AuthenticatorRepo) SetStatus(ctx context.Context, tx *gorm.DB, id uuid.UUID, status string) (int64, error) {
	switch status {
	case "active", "disabled", "revoked":
	default:
		return 0, errors.New("store: authenticator status must be active|disabled|revoked")
	}
	db := r.db
	if tx != nil {
		db = tx
	}
	res := db.WithContext(ctx).Model(&model.Authenticator{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// ConsumeTOTPStep 是**动态口令重放防护的落地点**：把已消费的时间步原子推进到 step。
//
// SQL 与 `internal/credential` 里写下的契约**逐字一致**：
//
//	UPDATE authenticators SET credential_data = jsonb_set(...,'{last_step}', to_jsonb($2))
//	 WHERE id = $1 AND COALESCE((credential_data->>'last_step')::bigint, 0) < $2
//
// 为什么必须是一条带条件的 UPDATE（而不是"先读再写"）：两个并发请求都会读到同一个
// `last_step`，然后都写成功——那等于没有防重放。影响行数 0 = 被别人抢先消费 → 重放。
func (r *AuthenticatorRepo) ConsumeTOTPStep(ctx context.Context, tx *gorm.DB, id uuid.UUID, step int64) (int64, error) {
	if step <= 0 {
		return 0, errors.New("store: totp step must be > 0")
	}
	db := r.db
	if tx != nil {
		db = tx
	}
	res := db.WithContext(ctx).Exec(`
		UPDATE authenticators
		   SET credential_data = jsonb_set(credential_data, '{last_step}', to_jsonb(?::bigint)),
		       last_used_at = now()
		 WHERE id = ?
		   AND COALESCE((credential_data->>'last_step')::bigint, 0) < ?`, step, id, step)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}
