package identifier

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvariantI1AtLeastOneVerifiedIdentifier(t *testing.T) {
	require.ErrorIs(t, CheckUnbind(UnbindCounts{TotalAfter: 1, VerifiedAfter: 0}), ErrLastIdentifierCannotUnbind)
	require.NoError(t, CheckUnbind(UnbindCounts{TotalAfter: 2, VerifiedAfter: 1}))
}

func TestInvariantI2CannotUnbindLastIdentifier(t *testing.T) {
	require.ErrorIs(t, CheckUnbind(UnbindCounts{TotalAfter: 0, VerifiedAfter: 0}), ErrLastIdentifierCannotUnbind)
	require.NoError(t, CheckUnbind(UnbindCounts{TotalAfter: 1, VerifiedAfter: 1}))
}

func TestInvariantI3UsableCapabilityUnion(t *testing.T) {
	require.Equal(t, 0, UsableAuthCapability(0, 0))
	require.Equal(t, 1, UsableAuthCapability(1, 0))
	require.Equal(t, 1, UsableAuthCapability(0, 1))
	require.Equal(t, 2, UsableAuthCapability(1, 1))
	require.ErrorIs(t, CheckUsableCapability(0, 0), ErrLastAuthenticatorCannotRemove)
	require.NoError(t, CheckUsableCapability(0, 1))
	require.NoError(t, CheckUsableCapability(1, 0))
}

func TestInvariantI4CannotRemoveLastAuthenticator(t *testing.T) {
	require.ErrorIs(t, CheckUsableCapability(0, 0), ErrLastAuthenticatorCannotRemove)
	require.NoError(t, CheckUsableCapability(0, 1))
}

func TestInvariantI6CannotDemoteLastPrimary(t *testing.T) {
	require.ErrorIs(t, CheckPrimaryChange(0), ErrLastPrimaryCannotDemote)
	require.NoError(t, CheckPrimaryChange(1))
	require.NoError(t, CheckPrimaryChange(2))
}
