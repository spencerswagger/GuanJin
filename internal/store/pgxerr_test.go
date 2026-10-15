package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func pgErr(code, constraint string) error {
	return &pgconn.PgError{Code: code, ConstraintName: constraint}
}

func TestMapPgErrorUniqueConstraints(t *testing.T) {
	cases := map[string]error{
		"identifiers_type_value_uniq": ErrIdentifierAlreadyBound,
		"users_username_lower_uidx":   ErrIdentifierAlreadyBound,
		"clients_pkey":                ErrClientIDAlreadyExists,
		"signing_keys_kid_key":        ErrKeyKidExists,
		"role_bindings_uniq":          ErrBindingAlreadyExists,
		"policy_snapshots_uniq":       ErrPolicySnapshotConflict,
		"user_visited_clients_uniq":   ErrConflict,
	}
	for constraint, want := range cases {
		t.Run(constraint, func(t *testing.T) {
			got := MapPgError(pgErr("23505", constraint))
			require.ErrorIs(t, got, want)
		})
	}
}

func TestMapPgErrorUnclassifiedUnique(t *testing.T) {
	require.ErrorIs(t, MapPgError(pgErr("23505", "some_other_uniq")), ErrConflict)
}

func TestMapPgErrorForeignKey(t *testing.T) {
	require.ErrorIs(t, MapPgError(pgErr("23503", "identifiers_user_id_fkey")), ErrReferenceNotFound)
}

func TestMapPgErrorCheckViolation(t *testing.T) {
	require.ErrorIs(t, MapPgError(pgErr("23514", "runtime_config_no_master_key")), ErrInvalidParameter)
}

func TestMapPgErrorPassThrough(t *testing.T) {
	require.NoError(t, MapPgError(nil))

	plain := errors.New("boom")
	require.ErrorIs(t, MapPgError(plain), plain)

	other := pgErr("42P01", "")
	require.ErrorIs(t, MapPgError(other), other)
}
