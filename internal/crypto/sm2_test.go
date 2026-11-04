package crypto

import (
	"errors"
	"testing"
)

func TestSM2SignVerify(t *testing.T) {
	priv, err := GenerateSM2Key(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pub := &priv.PublicKey

	msg := []byte("canonical-payload-to-sign")
	r, s, err := SignSM2(priv, msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if r == nil || s == nil {
		t.Fatal("signature digits must not be nil")
	}
	if !VerifySM2(pub, msg, r, s) {
		t.Fatal("valid signature must verify")
	}

	// 篡改消息 / 篡改签名数字 / 换钥 → 必须验不过。
	if VerifySM2(pub, []byte("canonical-payload-to-sign!"), r, s) {
		t.Fatal("tampered message must not verify")
	}
	other, err := GenerateSM2Key(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if VerifySM2(&other.PublicKey, msg, r, s) {
		t.Fatal("signature from another key must not verify")
	}
	if VerifySM2(pub, msg, nil, s) || VerifySM2(pub, msg, r, nil) || VerifySM2(nil, msg, r, s) {
		t.Fatal("nil inputs must verify false, not panic")
	}
}

// 空消息与长消息都能签（SM2 对消息长度无限制）。
func TestSM2SignEdgeCases(t *testing.T) {
	priv, err := GenerateSM2Key(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	for _, msg := range [][]byte{nil, []byte(""), make([]byte, 8192)} {
		r, s, err := SignSM2(priv, msg)
		if err != nil {
			t.Fatalf("sign(len=%d): %v", len(msg), err)
		}
		if !VerifySM2(&priv.PublicKey, msg, r, s) {
			t.Fatalf("signature for len=%d must verify", len(msg))
		}
	}
	if _, _, err := SignSM2(nil, []byte("x")); !errors.Is(err, ErrNilSM2Key) {
		t.Fatalf("nil key err = %v, want ErrNilSM2Key", err)
	}
}

// 压缩公钥往返（JWKS 用）：33 字节，前缀 0x02/0x03。
func TestSM2PublicKeyMarshalRoundTrip(t *testing.T) {
	priv, err := GenerateSM2Key(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	raw, err := MarshalSM2PublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) != 33 || (raw[0] != 0x02 && raw[0] != 0x03) {
		t.Fatalf("compressed key must be 33 bytes with 0x02/0x03 prefix, got %x", raw)
	}
	pub, err := UnmarshalSM2PublicKey(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	r, s, err := SignSM2(priv, []byte("m"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !VerifySM2(pub, []byte("m"), r, s) {
		t.Fatal("key restored from compressed form must verify")
	}
}

// 长度/前缀不对必须**明确报错**（gmsm 的 Decompress 对垃圾输入不报错，这里要挡住）。
func TestSM2PublicKeyRejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{
		nil,
		{},
		make([]byte, 32),
		make([]byte, 34),
		append([]byte{0x04}, make([]byte, 32)...), // 未压缩前缀
	} {
		if _, err := UnmarshalSM2PublicKey(bad); err == nil {
			t.Errorf("len=%d prefix=%x must be rejected", len(bad), bad[:min(1, len(bad))])
		}
	}
	if _, err := MarshalSM2PublicKey(nil); !errors.Is(err, ErrNilSM2Key) {
		t.Fatalf("nil pubkey err = %v, want ErrNilSM2Key", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
