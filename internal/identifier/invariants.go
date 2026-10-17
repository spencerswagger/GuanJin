package identifier

import "errors"

var (
	ErrLastIdentifierCannotUnbind    = errors.New("LAST_IDENTIFIER_CANNOT_UNBIND")
	ErrLastAuthenticatorCannotRemove = errors.New("LAST_AUTHENTICATOR_CANNOT_REMOVE")
	ErrLastPrimaryCannotDemote       = errors.New("IAM_IDENT_LAST_PRIMARY")
)

type UnbindCounts struct {
	TotalAfter    int
	VerifiedAfter int
}

func CheckUnbind(c UnbindCounts) error {
	if c.TotalAfter < 1 {
		return ErrLastIdentifierCannotUnbind
	}
	if c.VerifiedAfter < 1 {
		return ErrLastIdentifierCannotUnbind
	}
	return nil
}

func UsableAuthCapability(activeHoldingAuthenticators, verifiedAuthIdentifiers int) int {
	return activeHoldingAuthenticators + verifiedAuthIdentifiers
}

func CheckUsableCapability(activeHoldingAuthenticators, verifiedAuthIdentifiers int) error {
	if UsableAuthCapability(activeHoldingAuthenticators, verifiedAuthIdentifiers) < 1 {
		return ErrLastAuthenticatorCannotRemove
	}
	return nil
}

func CheckPrimaryChange(primaryAfter int) error {
	if primaryAfter < 1 {
		return ErrLastPrimaryCannotDemote
	}
	return nil
}