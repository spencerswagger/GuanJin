package store

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	signingKeyLockNamespace = 0x53494731
	signingKeyLockOIDC      = 1
	signingKeyLockGateway   = 2
)

type SigningKeyRepo struct {
	db        *gorm.DB
	masterKey []byte
}

func NewSigningKeyRepo(db *gorm.DB, masterKey []byte) *SigningKeyRepo {
	return &SigningKeyRepo{db: db, masterKey: masterKey}
}

func privateKeyCipherKey(masterKey []byte) []byte {
	return crypto.Derive(masterKey, "guanjin/sm4/private-key", 16)
}

func DecryptPrivateKey(masterKey, enc []byte) (*sm2.PrivateKey, error) {
	raw, err := crypto.SM4Decrypt(privateKeyCipherKey(masterKey), enc)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(raw)
}

func EncryptPrivateKey(masterKey []byte, priv *sm2.PrivateKey) ([]byte, error) {
	raw := crypto.MarshalPrivateKey(priv)
	if raw == nil {
		return nil, crypto.ErrSM2InvalidKey
	}
	return crypto.SM4Encrypt(privateKeyCipherKey(masterKey), raw)
}

func PublicKeyToText(pub *sm2.PublicKey) string {
	return base64.StdEncoding.EncodeToString(crypto.MarshalPublicKey(pub))
}

func PublicKeyFromText(text string) (*sm2.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPublicKey(raw)
}

func signingKeyLockOrdinal(purpose string) int {
	if purpose == crypto.PurposeGateway {
		return signingKeyLockGateway
	}
	return signingKeyLockOIDC
}

func (r *SigningKeyRepo) EnsureKeypair(ctx context.Context, purpose string) (*model.SigningKey, error) {
	if purpose != crypto.PurposeOIDC && purpose != crypto.PurposeGateway {
		return nil, ErrInvalidParameter
	}

	var out *model.SigningKey
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", signingKeyLockNamespace, signingKeyLockOrdinal(purpose)).Error; err != nil {
			return err
		}

		existing, err := getActiveByPurpose(tx, purpose)
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		priv, err := crypto.GenerateSM2Keypair()
		if err != nil {
			return err
		}
		enc, err := EncryptPrivateKey(r.masterKey, priv)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		kid, err := uuid.NewV7()
		if err != nil {
			return err
		}
		row := &model.SigningKey{
			Kid:           crypto.KIDPrefixForPurpose(purpose) + kid.String(),
			Purpose:       purpose,
			Algorithm:     crypto.AlgForPurpose(purpose),
			Status:        "active",
			PublicKey:     PublicKeyToText(&priv.PublicKey),
			PrivateKeyEnc: enc,
			ActivatesAt:   &now,
		}
		if err := MapPgError(tx.Create(row).Error); err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func getActiveByPurpose(tx *gorm.DB, purpose string) (*model.SigningKey, error) {
	var row model.SigningKey
	res := tx.Where("purpose = ? AND status = ?", purpose, "active").
		Order("created_at DESC").
		Limit(1).
		Find(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &row, nil
}

func (r *SigningKeyRepo) GetActiveByPurpose(ctx context.Context, purpose string) (*model.SigningKey, error) {
	row, err := getActiveByPurpose(r.db.WithContext(ctx), purpose)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSigningKeyNotFound
	}
	return row, err
}

func (r *SigningKeyRepo) ListPublishable(ctx context.Context, purpose string) ([]model.SigningKey, error) {
	var rows []model.SigningKey
	err := r.db.WithContext(ctx).
		Where("purpose = ? AND status IN ?", purpose, []string{"active", "retiring"}).
		Order("created_at DESC").
		Find(&rows).Error
	return rows, err
}

func (r *SigningKeyRepo) ActiveSigner(ctx context.Context, purpose string) (string, *sm2.PrivateKey, error) {
	row, err := r.GetActiveByPurpose(ctx, purpose)
	if err != nil {
		return "", nil, err
	}
	priv, err := DecryptPrivateKey(r.masterKey, row.PrivateKeyEnc)
	if err != nil {
		return "", nil, err
	}
	return row.Kid, priv, nil
}
