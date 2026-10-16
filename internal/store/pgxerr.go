package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrIdentifierAlreadyBound = errors.New("IDENTIFIER_ALREADY_BOUND")
	ErrClientIDAlreadyExists  = errors.New("CLIENT_ID_ALREADY_EXISTS")
	ErrKeyKidExists           = errors.New("KEY_KID_EXISTS")
	ErrBindingAlreadyExists   = errors.New("BINDING_ALREADY_EXISTS")
	ErrPolicySnapshotConflict = errors.New("POLICY_SNAPSHOT_CONFLICT")
	ErrReferenceNotFound      = errors.New("REFERENCE_NOT_FOUND")
	ErrInvalidParameter       = errors.New("INVALID_PARAMETER")
	ErrConflict               = errors.New("CONFLICT")

	ErrUserNotFound       = errors.New("USER_NOT_FOUND")
	ErrClientNotFound     = errors.New("CLIENT_NOT_FOUND")
	ErrSessionNotFound    = errors.New("SESSION_NOT_FOUND")
	ErrIdentifierNotFound = errors.New("IDENTIFIER_NOT_FOUND")
	ErrSigningKeyNotFound = errors.New("KEY_UNAVAILABLE")
	ErrInvalidCredential  = errors.New("INVALID_CREDENTIAL")
)

func MapPgError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		switch pgErr.ConstraintName {
		case "identifiers_type_value_uniq":
			return ErrIdentifierAlreadyBound
		case "users_username_lower_uidx":
			return ErrIdentifierAlreadyBound
		case "clients_pkey":
			return ErrClientIDAlreadyExists
		case "signing_keys_kid_key":
			return ErrKeyKidExists
		case "role_bindings_uniq":
			return ErrBindingAlreadyExists
		case "policy_snapshots_uniq":
			return ErrPolicySnapshotConflict
		case "user_visited_clients_uniq":
			return ErrConflict
		default:
			return ErrConflict
		}
	case "23503":
		return ErrReferenceNotFound
	case "23514":
		return ErrInvalidParameter
	default:
		return err
	}
}
