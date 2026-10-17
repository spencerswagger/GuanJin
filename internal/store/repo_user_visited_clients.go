package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type UserVisitedClientRepo struct {
	db *gorm.DB
}

func NewUserVisitedClientRepo(db *gorm.DB) *UserVisitedClientRepo {
	return &UserVisitedClientRepo{db: db}
}

func (r *UserVisitedClientRepo) Upsert(ctx context.Context, userID uuid.UUID, clientID, tierAtVisit string) error {
	return upsertVisitedClient(r.db.WithContext(ctx), userID, clientID, tierAtVisit)
}

func upsertVisitedClient(tx *gorm.DB, userID uuid.UUID, clientID, tierAtVisit string) error {
	now := time.Now().UTC()
	row := &model.UserVisitedClient{
		UserID:      userID,
		ClientID:    clientID,
		VisitCount:  1,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	if tierAtVisit != "" {
		tier := tierAtVisit
		row.TierAtVisit = &tier
	}
	updates := map[string]any{
		"visit_count":  gorm.Expr("user_visited_clients.visit_count + 1"),
		"last_seen_at": now,
		"updated_at":   now,
	}
	if tierAtVisit != "" {
		updates["tier_at_visit"] = tierAtVisit
	}
	return MapPgError(tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "client_id"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(row).Error)
}

func (r *UserVisitedClientRepo) MaxTierForUser(ctx context.Context, userID uuid.UUID) (string, bool, error) {
	var tier string
	res := r.db.WithContext(ctx).Raw(`
		SELECT c.tier FROM user_visited_clients v
		JOIN clients c ON c.client_id = v.client_id
		WHERE v.user_id = ?
		ORDER BY CASE c.tier WHEN 'L3' THEN 3 WHEN 'L2' THEN 2 WHEN 'L1' THEN 1 ELSE 0 END DESC
		LIMIT 1`, userID).Scan(&tier)
	if res.Error != nil {
		return "", false, res.Error
	}
	if res.RowsAffected == 0 || tier == "" {
		return "", false, nil
	}
	return tier, true, nil
}