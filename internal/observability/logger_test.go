package observability

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestJSONLoggerUsesFixedFieldNames(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "prod", "info", "json", "guanjind")
	require.NoError(t, err)

	logger.Info("access",
		zap.String(FieldMethod, "GET"),
		zap.String(FieldPath, "/healthz"),
		zap.Int(FieldStatus, 200),
	)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))

	require.Contains(t, entry, FieldTS)
	require.Contains(t, entry, FieldLevel)
	require.Contains(t, entry, FieldMsg)
	require.Equal(t, "guanjind", entry[FieldService])
	require.Equal(t, "prod", entry[FieldEnv])
	require.Equal(t, "info", entry[FieldLevel])
	require.Equal(t, "access", entry[FieldMsg])
	require.Equal(t, "GET", entry[FieldMethod])
}
