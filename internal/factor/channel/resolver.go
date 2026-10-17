package channel

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/store"
)

type IdentifierResolver struct {
	identifiers *store.IdentifierRepo
	blind       *identifier.BlindIndexer
	typ         string
}

func NewIdentifierResolver(identifiers *store.IdentifierRepo, blind *identifier.BlindIndexer, typ string) *IdentifierResolver {
	return &IdentifierResolver{identifiers: identifiers, blind: blind, typ: typ}
}

func (r *IdentifierResolver) ResolveTarget(ctx context.Context, userID string) (string, string, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", "", errors.New("IDENTIFIER_NOT_FOUND")
	}
	rows, err := r.identifiers.ListByUser(ctx, uid)
	if err != nil {
		return "", "", err
	}
	for _, row := range rows {
		if row.Type != r.typ || !row.Verified || row.Status != "active" {
			continue
		}
		target, decErr := r.blind.Decrypt(row.ValueEnc)
		if decErr != nil {
			continue
		}
		return target, row.ValueMasked, nil
	}
	return "", "", errors.New("IDENTIFIER_NOT_FOUND")
}