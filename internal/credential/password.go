package credential

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
	"unicode"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	DefaultMinLength    = 12
	DefaultCharClasses  = 3
	DefaultHistoryCount = 5
	defaultIterations   = 210000
)

var (
	ErrPasswordPolicyViolation = errors.New("PASSWORD_POLICY_VIOLATION")
	ErrPasswordReused          = errors.New("PASSWORD_REUSED")
	ErrUnsupportedAlgo         = errors.New("unsupported credential algorithm")
)

type Policy struct {
	MinLength    int
	CharClasses  int
	HistoryCount int
}

func DefaultPolicy() Policy {
	return Policy{MinLength: DefaultMinLength, CharClasses: DefaultCharClasses, HistoryCount: DefaultHistoryCount}
}

func PolicyFromLoginPolicy(p *model.LoginPolicy) Policy {
	if p == nil {
		return DefaultPolicy()
	}
	policy := Policy{
		MinLength:    int(p.PasswordMinLength),
		CharClasses:  int(p.PasswordCharClasses),
		HistoryCount: int(p.PasswordHistoryCount),
	}
	if policy.MinLength <= 0 {
		policy.MinLength = DefaultMinLength
	}
	if policy.CharClasses <= 0 {
		policy.CharClasses = DefaultCharClasses
	}
	if policy.HistoryCount < 0 {
		policy.HistoryCount = DefaultHistoryCount
	}
	return policy
}

type credentialData struct {
	Algo       string `json:"algo"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	DK         string `json:"dk"`
	KeyLen     int    `json:"key_len"`
	UpdatedAt  string `json:"updated_at"`
}

func Hash(password string, policy Policy) ([]byte, error) {
	if err := ValidatePolicy(password, policy); err != nil {
		return nil, err
	}
	salt, err := crypto.NewPBKDF2Salt()
	if err != nil {
		return nil, err
	}
	dk := crypto.PBKDF2SM3([]byte(password), salt, defaultIterations, crypto.DefaultPBKDF2KeyLen)
	data := credentialData{
		Algo:       crypto.PBKDF2Algo,
		Iterations: defaultIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		DK:         base64.StdEncoding.EncodeToString(dk),
		KeyLen:     crypto.DefaultPBKDF2KeyLen,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(data)
}

func Verify(stored, password []byte) (bool, error) {
	var data credentialData
	if err := json.Unmarshal(stored, &data); err != nil {
		return false, err
	}
	if data.Algo != crypto.PBKDF2Algo {
		return false, ErrUnsupportedAlgo
	}
	salt, err := base64.StdEncoding.DecodeString(data.Salt)
	if err != nil {
		return false, err
	}
	expected, err := base64.StdEncoding.DecodeString(data.DK)
	if err != nil {
		return false, err
	}
	keyLen := data.KeyLen
	if keyLen <= 0 {
		keyLen = len(expected)
	}
	iterations := data.Iterations
	if iterations < crypto.MinPBKDF2Iterations {
		iterations = crypto.MinPBKDF2Iterations
	}
	got := crypto.PBKDF2SM3(password, salt, iterations, keyLen)
	return subtle.ConstantTimeCompare(got, expected) == 1, nil
}

func ValidatePolicy(password string, policy Policy) error {
	minLength := policy.MinLength
	if minLength <= 0 {
		minLength = DefaultMinLength
	}
	if len([]rune(password)) < minLength {
		return ErrPasswordPolicyViolation
	}
	charClasses := policy.CharClasses
	if charClasses <= 0 {
		charClasses = DefaultCharClasses
	}
	if countCharClasses(password) < charClasses {
		return ErrPasswordPolicyViolation
	}
	return nil
}

func CheckHistory(password string, history [][]byte) error {
	limit := DefaultHistoryCount
	if len(history) > limit {
		history = history[:limit]
	}
	for _, previous := range history {
		ok, err := Verify(previous, []byte(password))
		if err != nil {
			continue
		}
		if ok {
			return ErrPasswordReused
		}
	}
	return nil
}

func countCharClasses(password string) int {
	var lower, upper, digit, symbol bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	count := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			count++
		}
	}
	return count
}
