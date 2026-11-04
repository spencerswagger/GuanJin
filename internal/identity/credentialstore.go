package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/store"
)

// CredentialStore 把 `authenticators` 表接到因子层的窄接口上
// （`holding.PasswordStore` / `holding.TOTPStore`）。
//
// 为什么单独一个类型而不是直接给因子塞 `*gorm.DB`：
//   - 因子只该知道"怎么验"，不该知道"哪张表、哪个列"；
//   - 凭据信封的**解析即校验**（弱参数会被拒）在唯一的入口里做一次，
//     不会出现"某个因子忘了 Validate 就用了弱信封"。
type CredentialStore struct {
	db   *gorm.DB
	repo *store.AuthenticatorRepo
}

// NewCredentialStore 绑定连接。
func NewCredentialStore(db *gorm.DB) (*CredentialStore, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	return &CredentialStore{db: db, repo: store.NewAuthenticatorRepo(db)}, nil
}

// ListPasswordEnvelopes 实现 holding.PasswordStore。
//
// 只要 `type='password'` 的可用行；解析失败即报错（**不跳过**：跳过等于让一个坏凭据
// 静默变成"这个用户没有密码"，随后就是 `INVALID_CREDENTIAL`，排查时看不出根因）。
func (s *CredentialStore) ListPasswordEnvelopes(ctx context.Context, userID string) ([]credential.PasswordEnvelope, error) {
	uid, err := parseStrongUserID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListActiveByUserType(ctx, uid, string(factor.FactorPassword), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	out := make([]credential.PasswordEnvelope, 0, len(rows))
	for _, row := range rows {
		env, err := credential.ParsePasswordEnvelope(row.CredentialData)
		if err != nil {
			return nil, fmt.Errorf("identity: authenticator %s: %w", row.ID, err)
		}
		out = append(out, env)
	}
	return out, nil
}

// ListTOTPRecords 实现 holding.TOTPStore。
func (s *CredentialStore) ListTOTPRecords(ctx context.Context, userID string) ([]holding.TOTPRecord, error) {
	uid, err := parseStrongUserID(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListActiveByUserType(ctx, uid, string(factor.FactorTOTP), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	out := make([]holding.TOTPRecord, 0, len(rows))
	for _, row := range rows {
		env, err := credential.ParseTOTPEnvelope(row.CredentialData)
		if err != nil {
			return nil, fmt.Errorf("identity: authenticator %s: %w", row.ID, err)
		}
		out = append(out, holding.TOTPRecord{AuthenticatorID: row.ID.String(), Envelope: env})
	}
	return out, nil
}

// ConsumeStep 实现 holding.TOTPStore：把"已消费时间步"原子推进到 step。
//
// 返回 false 表示这一行已经被别人推进过（并发双花或同窗重放）——
// 调用方必须据此回 `TEMPORAL_CODE_REUSED`，**不能**因为"码对"就放行。
func (s *CredentialStore) ConsumeStep(ctx context.Context, authenticatorID string, step int64) (bool, error) {
	id, err := uuid.Parse(authenticatorID)
	if err != nil {
		return false, errors.New("identity: invalid authenticator id")
	}
	affected, err := s.repo.ConsumeTOTPStep(ctx, s.db, id, step)
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// parseStrongUserID 解析 UUID；失败一律按"凭据无效"处理（不泄露"这个字符串不是 UUID"）。
func parseStrongUserID(raw string) (uuid.UUID, error) {
	uid, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, credential.ErrInvalidCredential
	}
	if uid == uuid.Nil {
		return uuid.Nil, credential.ErrInvalidCredential
	}
	return uid, nil
}

// 断言：本类型满足因子层的两个窄接口（编译期钉住，改名即构建失败）。
var (
	_ holding.PasswordStore = (*CredentialStore)(nil)
	_ holding.TOTPStore     = (*CredentialStore)(nil)
)
