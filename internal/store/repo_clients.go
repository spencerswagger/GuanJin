package store

import (
	"context"
	"crypto/hmac"
	"errors"

	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type ClientRepo struct {
	db        *gorm.DB
	masterKey []byte
}

func NewClientRepo(db *gorm.DB, masterKey []byte) *ClientRepo {
	return &ClientRepo{db: db, masterKey: masterKey}
}

func clientSecretKey(masterKey []byte) []byte {
	return crypto.Derive(masterKey, "guanjin/hmac/client-secret", 32)
}

func clientSecretEncKey(masterKey []byte) []byte {
	return crypto.Derive(masterKey, "guanjin/sm4/client-secret", crypto.SM4KeySize)
}

func EncryptClientSecret(masterKey []byte, secret string) ([]byte, error) {
	return crypto.SM4Encrypt(clientSecretEncKey(masterKey), []byte(secret))
}

func (r *ClientRepo) DecryptSecret(client *model.Client) (string, error) {
	if client == nil || len(client.SecretEnc) == 0 {
		return "", ErrClientSecretUnavailable
	}
	plain, err := crypto.SM4Decrypt(clientSecretEncKey(r.masterKey), client.SecretEnc)
	if err != nil {
		return "", ErrClientSecretUnavailable
	}
	return string(plain), nil
}

func HashClientSecret(masterKey []byte, secret string) []byte {
	return crypto.HMACSM3(clientSecretKey(masterKey), []byte(secret))
}

func (r *ClientRepo) GetByID(ctx context.Context, clientID string) (*model.Client, error) {
	var client model.Client
	err := r.db.WithContext(ctx).Where("client_id = ?", clientID).First(&client).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, err
	}
	return &client, nil
}

func IsPublicClient(client *model.Client) bool {
	return client == nil || len(client.SecretHash) == 0
}

func (r *ClientRepo) VerifySecret(client *model.Client, secret string) bool {
	if client == nil || secret == "" {
		return false
	}
	candidate := HashClientSecret(r.masterKey, secret)
	if len(client.SecretHash) > 0 && hmac.Equal(candidate, client.SecretHash) {
		return true
	}
	if len(client.PrevSecretHash) > 0 && hmac.Equal(candidate, client.PrevSecretHash) {
		return true
	}
	return false
}
