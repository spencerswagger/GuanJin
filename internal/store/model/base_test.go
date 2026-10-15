package model

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBaseModelBeforeCreateGeneratesUUIDv7(t *testing.T) {
	m := &BaseModel{}
	require.NoError(t, m.BeforeCreate(nil))
	require.NotEqual(t, uuid.Nil, m.ID)
	require.Equal(t, uuid.Version(7), m.ID.Version())
}

func TestBaseModelKeepsExistingID(t *testing.T) {
	existing := uuid.Must(uuid.NewV7())
	m := &BaseModel{ID: existing}
	require.NoError(t, m.BeforeCreate(nil))
	require.Equal(t, existing, m.ID)
}

func TestAppendOnlyModelBeforeCreateGeneratesUUIDv7(t *testing.T) {
	m := &AppendOnlyModel{}
	require.NoError(t, m.BeforeCreate(nil))
	require.NotEqual(t, uuid.Nil, m.ID)
	require.Equal(t, uuid.Version(7), m.ID.Version())
}
