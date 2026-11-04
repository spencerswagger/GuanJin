package store

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SigningKeyRow 是 `signing_keys` 的一行（只取签发需要的列）。
//
// 不放在 `model` 包里：这四列只被 `internal/signing` 消费，模型化会诱使别的包
// 直接拿它当"业务对象"用（签名密钥没有业务语义，只有"能不能签"）。
type SigningKeyRow struct {
	Kid        string `gorm:"column:kid"`
	Purpose    string `gorm:"column:purpose"`
	Algorithm  string `gorm:"column:algorithm"`
	PublicKey  string `gorm:"column:public_key"`
	PrivateEnc []byte `gorm:"column:private_key_enc"`
	Status     string `gorm:"column:status"`
}

// SigningKeyRepo 读写 `signing_keys`（S6：加载与 keygen 入库；轮换状态机归后续切片）。
type SigningKeyRepo struct{ db *gorm.DB }

// NewSigningKeyRepo 绑定连接。
func NewSigningKeyRepo(db *gorm.DB) *SigningKeyRepo { return &SigningKeyRepo{db: db} }

// ListUsable 取**可用于签发**的密钥行（`status='active'`）。
//
// 启动期若某用途没有行，由调用方 fail-fast（`signing.ErrNoActiveKey`）；
// 这里不返回错误而是空切片——"没有密钥"与"读库失败"必须能被分开处置。
func (r *SigningKeyRepo) ListUsable(ctx context.Context) ([]SigningKeyRow, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: nil db")
	}
	var rows []SigningKeyRow
	res := r.db.WithContext(ctx).Raw(
		`SELECT kid, purpose, algorithm, public_key, private_key_enc, status
		   FROM signing_keys WHERE status = 'active' ORDER BY purpose, kid`).Scan(&rows)
	if res.Error != nil {
		return nil, MapError(res.Error)
	}
	return rows, nil
}

// InsertIfAbsent 幂等插入一把密钥（`kid` 唯一索引冲突即视为"已存在"，返回 inserted=false）。
//
// keygen 的幂等就落在这里：重复执行 `guanjind keygen` 不得覆盖既有密钥
// （覆盖 = 静默换签名键 = 把在途 token 全废）。
func (r *SigningKeyRepo) InsertIfAbsent(ctx context.Context, row SigningKeyRow) (inserted bool, err error) {
	if r == nil || r.db == nil {
		return false, errors.New("store: nil db")
	}
	if row.Kid == "" || row.Purpose == "" || row.Algorithm == "" {
		return false, errors.New("store: signing key requires kid/purpose/algorithm")
	}
	// 主键由应用层生成（D4-A：迁移里不写 DEFAULT uuidv7()）。
	id, idErr := uuid.NewV7()
	if idErr != nil {
		return false, idErr
	}
	res := r.db.WithContext(ctx).Exec(
		`INSERT INTO signing_keys (id, kid, purpose, algorithm, status, public_key, private_key_enc)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)
		 ON CONFLICT (kid) DO NOTHING`,
		id, row.Kid, row.Purpose, row.Algorithm, row.PublicKey, row.PrivateEnc)
	if res.Error != nil {
		return false, MapError(res.Error)
	}
	return res.RowsAffected == 1, nil
}

var _ = clause.OnConflict{}
