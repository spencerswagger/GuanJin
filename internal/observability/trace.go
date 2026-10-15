package observability

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

type traceContextKey struct{}

func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceContextKey{}, traceID)
}

func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if tid, ok := ctx.Value(traceContextKey{}).(string); ok {
		return tid
	}
	return ""
}

func NewTraceID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func ParseTraceparent(header string) string {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 {
		return ""
	}
	version, traceID, spanID, flags := parts[0], parts[1], parts[2], parts[3]
	if version == "ff" {
		return ""
	}
	if len(version) != 2 || len(traceID) != 32 || len(spanID) != 16 || len(flags) != 2 {
		return ""
	}
	if traceID == strings.Repeat("0", 32) || spanID == strings.Repeat("0", 16) {
		return ""
	}
	if !isLowerHex(traceID) || !isLowerHex(spanID) || !isLowerHex(flags) {
		return ""
	}
	return traceID
}

func ResolveTraceID(traceparent string) string {
	if tid := ParseTraceparent(traceparent); tid != "" {
		return tid
	}
	return NewTraceID()
}

func Traceparent(traceID string) string {
	if !isLowerHex(traceID) || len(traceID) != 32 {
		return ""
	}
	span, err := uuid.NewV7()
	if err != nil {
		return ""
	}
	spanID := strings.ReplaceAll(span.String(), "-", "")[16:32]
	return "00-" + traceID + "-" + spanID + "-01"
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
