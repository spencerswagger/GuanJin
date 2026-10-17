package gateway

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store"
)

const (
	AssertionIssuer = "guanjin-gateway"
	AssertionTTL    = 60 * time.Second
)

type ClaimInput struct {
	Subject      string
	Audience     string
	OriginClient string
	Username     string
	Roles        []string
	ACR          string
	AMR          []string
}

type Assertor interface {
	Issue(ctx context.Context, in ClaimInput) (string, string, error)
}

type SM2Assertor struct {
	keys   *store.SigningKeyRepo
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

func NewSM2Assertor(keys *store.SigningKeyRepo, issuer string, ttl time.Duration) *SM2Assertor {
	if issuer == "" {
		issuer = AssertionIssuer
	}
	if ttl <= 0 {
		ttl = AssertionTTL
	}
	return &SM2Assertor{
		keys:   keys,
		issuer: issuer,
		ttl:    ttl,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (a *SM2Assertor) Issue(ctx context.Context, in ClaimInput) (string, string, error) {
	kid, priv, err := a.keys.ActiveSigner(ctx, crypto.PurposeGateway)
	if err != nil {
		return "", "", err
	}
	now := a.now()
	jti, err := uuid.NewV7()
	if err != nil {
		return "", "", err
	}
	roles := in.Roles
	if roles == nil {
		roles = []string{}
	}
	amr := in.AMR
	if amr == nil {
		amr = []string{}
	}
	claims := jwt.MapClaims{
		"iss":   a.issuer,
		"sub":   in.Subject,
		"aud":   in.Audience,
		"cid":   in.OriginClient,
		"uname": in.Username,
		"roles": roles,
		"acr":   in.ACR,
		"amr":   amr,
		"iat":   now.Unix(),
		"exp":   now.Add(a.ttl).Unix(),
		"jti":   jti.String(),
	}
	token := jwt.NewWithClaims(crypto.SigningMethodSM3WithSM2, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		return "", "", err
	}
	return signed, kid, nil
}
