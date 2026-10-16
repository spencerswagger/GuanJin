package factor

type FactorType = string

type Kind string

const (
	KindHolding  Kind = "holding"
	KindChannel  Kind = "channel"
	KindProofing Kind = "proofing"
)

func (k Kind) Valid() bool {
	switch k {
	case KindHolding, KindChannel, KindProofing:
		return true
	default:
		return false
	}
}

type Provider interface {
	Type() FactorType
	Kind() Kind
	Descriptor() Descriptor
}
