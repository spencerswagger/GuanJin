package observability

import (
	"context"
	"io"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	FieldTS        = "ts"
	FieldLevel     = "level"
	FieldService   = "service"
	FieldEnv       = "env"
	FieldTraceID   = "trace_id"
	FieldSpan      = "span"
	FieldMethod    = "method"
	FieldPath      = "path"
	FieldStatus    = "status"
	FieldLatencyMS = "latency_ms"
	FieldClientID  = "client_id"
	FieldUserID    = "user_id"
	FieldSessionID = "session_id"
	FieldEventType = "event_type"
	FieldErrCode   = "err_code"
	FieldMsg       = "msg"
)

func NewLogger(envName, level, format, service string) (*zap.Logger, error) {
	return newLogger(os.Stdout, envName, level, format, service)
}

func newLogger(w io.Writer, envName, level, format, service string) (*zap.Logger, error) {
	cfg := zapcore.EncoderConfig{
		TimeKey:        FieldTS,
		LevelKey:       FieldLevel,
		MessageKey:     FieldMsg,
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     encodeRFC3339UTC,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if format == "console" {
		encoder = zapcore.NewConsoleEncoder(cfg)
	} else {
		encoder = zapcore.NewJSONEncoder(cfg)
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(w), parseLevel(level))
	logger := zap.New(core, zap.AddCaller()).With(
		zap.String(FieldService, service),
		zap.String(FieldEnv, envName),
	)
	return logger, nil
}

func TraceIDField(ctx context.Context) zap.Field {
	return zap.String(FieldTraceID, FromContext(ctx))
}

func parseLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

func encodeRFC3339UTC(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.UTC().Format(time.RFC3339))
}
