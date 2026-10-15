package config

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

const (
	EnvDev  = "dev"
	EnvProd = "prod"

	MinMasterKeyBytes = 32
)

type Env struct {
	Env       string `env:"GUANJIN_ENV" envDefault:"prod"`
	LogLevel  string `env:"GUANJIN_LOG_LEVEL" envDefault:"info"`
	LogFormat string `env:"GUANJIN_LOG_FORMAT" envDefault:"json"`

	HTTPAddr    string `env:"GUANJIN_HTTP_ADDR" envDefault:":8080"`
	GatewayAddr string `env:"GUANJIN_GATEWAY_ADDR" envDefault:":8081"`

	PublicBaseURL string `env:"GUANJIN_PUBLIC_BASE_URL,required"`
	Issuer        string `env:"GUANJIN_ISSUER"`

	DatabaseURL       string        `env:"GUANJIN_DATABASE_URL,required"`
	DBMaxOpenConns    int           `env:"GUANJIN_DB_MAX_OPEN_CONNS" envDefault:"50"`
	DBMaxIdleConns    int           `env:"GUANJIN_DB_MAX_IDLE_CONNS" envDefault:"10"`
	DBConnMaxLifetime time.Duration `env:"GUANJIN_DB_CONN_MAX_LIFETIME" envDefault:"30m"`

	RedisURL     string        `env:"GUANJIN_REDIS_URL,required"`
	RedisTimeout time.Duration `env:"GUANJIN_REDIS_TIMEOUT" envDefault:"2s"`

	MasterKey string `env:"GUANJIN_MASTER_KEY,required"`

	BootstrapAdminUsername string `env:"GUANJIN_BOOTSTRAP_ADMIN_USERNAME"`
	BootstrapAdminPassword string `env:"GUANJIN_BOOTSTRAP_ADMIN_PASSWORD"`

	GatewayUpstreamTimeout time.Duration `env:"GUANJIN_GATEWAY_UPSTREAM_TIMEOUT" envDefault:"5s"`
	GatewayDialTimeout     time.Duration `env:"GUANJIN_GATEWAY_DIAL_TIMEOUT" envDefault:"2s"`

	MetricsAddr  string `env:"GUANJIN_METRICS_ADDR" envDefault:"127.0.0.1:9090"`
	OTelEndpoint string `env:"GUANJIN_OTEL_ENDPOINT"`
}

func Load() (Env, error) {
	var e Env
	if err := env.Parse(&e); err != nil {
		return e, err
	}
	if e.Issuer == "" {
		e.Issuer = e.PublicBaseURL
	}
	raw, err := base64.StdEncoding.DecodeString(e.MasterKey)
	if err != nil {
		return e, fmt.Errorf("GUANJIN_MASTER_KEY 不是合法 base64: %w", err)
	}
	if len(raw) < MinMasterKeyBytes {
		return e, fmt.Errorf("GUANJIN_MASTER_KEY 至少 %d 字节（当前 %d）", MinMasterKeyBytes, len(raw))
	}
	return e, nil
}

func (e *Env) IsDev() bool {
	return e.Env == EnvDev
}

func (e *Env) MasterKeyBytes() ([]byte, error) {
	return base64.StdEncoding.DecodeString(e.MasterKey)
}
