package config

import (
	"context"
	"strconv"
)

const (
	KeyRatelimitDefaultPerMinute = "ratelimit.default_per_minute"
	KeySMSSendDailyBudget        = "sms.daily_budget"
	KeyAuditRetentionDays        = "audit.retention_days"
	KeyLogShipEnabled            = "log.ship_enabled"
)

type RuntimeReader interface {
	Value(ctx context.Context, key string) (string, error)
}

type Runtime struct {
	reader RuntimeReader
}

func NewRuntime(reader RuntimeReader) *Runtime {
	return &Runtime{reader: reader}
}

func (r *Runtime) String(ctx context.Context, key, fallback string) (string, error) {
	v, err := r.reader.Value(ctx, key)
	if err != nil {
		return fallback, err
	}
	if v == "" {
		return fallback, nil
	}
	return v, nil
}

func (r *Runtime) Int(ctx context.Context, key string, fallback int) (int, error) {
	v, err := r.String(ctx, key, "")
	if err != nil {
		return fallback, err
	}
	if v == "" {
		return fallback, nil
	}
	n, convErr := strconv.Atoi(v)
	if convErr != nil {
		return fallback, convErr
	}
	return n, nil
}

func (r *Runtime) Bool(ctx context.Context, key string, fallback bool) (bool, error) {
	v, err := r.String(ctx, key, "")
	if err != nil {
		return fallback, err
	}
	if v == "" {
		return fallback, nil
	}
	b, convErr := strconv.ParseBool(v)
	if convErr != nil {
		return fallback, convErr
	}
	return b, nil
}
