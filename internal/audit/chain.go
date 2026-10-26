package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const auditChainLockKey int64 = 0x5349_414D_A001

var ErrChainBroken = errors.New("AUDIT_CHAIN_BROKEN")

type Entry struct {
	ActorUserID *uuid.UUID
	ActorRole   *string
	Action      string
	TargetType  *string
	TargetID    *string
	ClientID    *string
	Before      any
	After       any
	Result      string
	IP          *string
	TraceID     *string
	OccurredAt  time.Time
}

type Chain struct {
	db        *gorm.DB
	masterKey []byte
	now       func() time.Time
}

func NewChain(db *gorm.DB, masterKey []byte) *Chain {
	return &Chain{db: db, masterKey: masterKey, now: func() time.Time { return time.Now().UTC() }}
}

func chainKey(masterKey []byte) []byte {
	return crypto.Derive(masterKey, "guanjin/hmac/audit-chain", 32)
}

type canonicalRecord struct {
	ID          string  `json:"id"`
	ActorUserID *string `json:"actor_user_id"`
	ActorRole   *string `json:"actor_role"`
	Action      string  `json:"action"`
	TargetType  *string `json:"target_type"`
	TargetID    *string `json:"target_id"`
	ClientID    *string `json:"client_id"`
	Before      string  `json:"before"`
	After       string  `json:"after"`
	Result      string  `json:"result"`
	IP          *string `json:"ip"`
	TraceID     *string `json:"trace_id"`
	OccurredAt  string  `json:"occurred_at"`
}

func (c *Chain) Append(ctx context.Context, entry Entry) (*model.AuditLog, error) {
	occurredAt := entry.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = c.now()
	}
	occurredAt = occurredAt.UTC().Truncate(time.Microsecond)
	result := entry.Result
	if result == "" {
		result = "success"
	}
	before, err := marshalJSON(entry.Before)
	if err != nil {
		return nil, err
	}
	after, err := marshalJSON(entry.After)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	row := &model.AuditLog{
		AppendOnlyModel: model.AppendOnlyModel{ID: id},
		ActorUserID:     entry.ActorUserID,
		ActorRole:       entry.ActorRole,
		Action:          entry.Action,
		TargetType:      entry.TargetType,
		TargetID:        entry.TargetID,
		ClientID:        entry.ClientID,
		Before:          before,
		After:           after,
		Result:          result,
		IP:              entry.IP,
		TraceID:         entry.TraceID,
		OccurredAt:      occurredAt,
	}

	err = c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", auditChainLockKey).Error; err != nil {
			return err
		}
		var prev []byte
		var prevAt sql.NullTime
		rowScan := tx.Raw("SELECT integrity_tag, occurred_at FROM audit_logs ORDER BY occurred_at DESC, id DESC LIMIT 1").Row()
		if err := rowScan.Scan(&prev, &prevAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(prev) == 0 {
			prev = nil
		}
		if prevAt.Valid && !row.OccurredAt.After(prevAt.Time) {
			row.OccurredAt = prevAt.Time.UTC().Add(time.Microsecond)
		}
		tag, err := c.tag(prev, row)
		if err != nil {
			return err
		}
		row.PrevTag = prev
		row.IntegrityTag = tag
		return store.MapPgError(tx.Create(row).Error)
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (c *Chain) Verify(ctx context.Context) error {
	var rows []model.AuditLog
	if err := c.db.WithContext(ctx).Order("occurred_at ASC, id ASC").Find(&rows).Error; err != nil {
		return err
	}
	var prev []byte
	for i := range rows {
		if !bytes.Equal(prev, rows[i].PrevTag) {
			return fmt.Errorf("%w: prev_tag mismatch at %s", ErrChainBroken, rows[i].ID)
		}
		tag, err := c.tag(prev, &rows[i])
		if err != nil {
			return err
		}
		if !hmac.Equal(tag, rows[i].IntegrityTag) {
			return fmt.Errorf("%w: integrity_tag mismatch at %s", ErrChainBroken, rows[i].ID)
		}
		prev = rows[i].IntegrityTag
	}
	return nil
}

func (c *Chain) tag(prev []byte, row *model.AuditLog) ([]byte, error) {
	canonical, err := canonicalize(row)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(prev)+len(canonical))
	payload = append(payload, prev...)
	payload = append(payload, canonical...)
	return crypto.HMACSM3(chainKey(c.masterKey), payload), nil
}

func canonicalize(row *model.AuditLog) ([]byte, error) {
	record := canonicalRecord{
		ID:          row.ID.String(),
		ActorUserID: uuidString(row.ActorUserID),
		ActorRole:   row.ActorRole,
		Action:      row.Action,
		TargetType:  row.TargetType,
		TargetID:    row.TargetID,
		ClientID:    row.ClientID,
		Before:      canonicalJSON(row.Before),
		After:       canonicalJSON(row.After),
		Result:      row.Result,
		IP:          row.IP,
		TraceID:     row.TraceID,
		OccurredAt:  row.OccurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
	}
	return json.Marshal(record)
}

func marshalJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func canonicalJSON(raw []byte) string {
	if len(raw) == 0 {
		return "null"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(normalized)
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}
