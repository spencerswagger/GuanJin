package admin

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type BootstrapDeps struct {
	DB             *gorm.DB
	Users          *store.UserRepo
	Authenticators *store.AuthenticatorRepo
	Username       string
	Password       string
}

func Bootstrap(ctx context.Context, deps BootstrapDeps) error {
	username := strings.TrimSpace(deps.Username)
	if username == "" || deps.Password == "" {
		return nil
	}

	user, err := deps.Users.GetByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrUserNotFound) {
		return err
	}
	if errors.Is(err, store.ErrUserNotFound) {
		data, hashErr := credential.Hash(deps.Password, credential.DefaultPolicy())
		if hashErr != nil {
			return hashErr
		}
		user = &model.User{Username: username, Status: "active", IsAdmin: true}
		if createErr := deps.Users.Create(ctx, user); createErr != nil {
			return createErr
		}
		return deps.Authenticators.Create(ctx, &model.Authenticator{
			UserID:         user.ID,
			Type:           "password",
			CredentialData: data,
			Status:         "active",
		})
	}

	if !user.IsAdmin {
		if err := deps.DB.WithContext(ctx).Model(&model.User{}).
			Where("id = ?", user.ID).
			Update("is_admin", true).Error; err != nil {
			return err
		}
	}
	if _, err := deps.Authenticators.GetActiveByUserType(ctx, user.ID, "password"); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrInvalidCredential) {
		return err
	}
	data, err := credential.Hash(deps.Password, credential.DefaultPolicy())
	if err != nil {
		return err
	}
	return deps.Authenticators.Create(ctx, &model.Authenticator{
		UserID:         user.ID,
		Type:           "password",
		CredentialData: data,
		Status:         "active",
	})
}
