package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store"
)

const nonceKeyPrefix = "guanjin:agent:nonce:"

func Canonical(method, path, sortedQuery, bodySHA256Hex string, timestamp int64, nonce string) string {
	return strings.Join([]string{method, path, sortedQuery, bodySHA256Hex, strconv.FormatInt(timestamp, 10), nonce}, "\n")
}

func SHA256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func Signature(secret []byte, canonical string) string {
	return base64.StdEncoding.EncodeToString(crypto.HMACSM3(secret, []byte(canonical)))
}

func (s *Service) authenticate(c *gin.Context) ([]byte, error) {
	body, err := readBody(c)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(c.GetHeader(HeaderClient))
	if clientID == "" {
		return nil, httpx.New("AGENT_UNKNOWN_CLIENT", "未知的业务系统")
	}
	client, err := s.clients.GetByID(c.Request.Context(), clientID)
	if err != nil {
		if errors.Is(err, store.ErrClientNotFound) {
			return nil, httpx.New("AGENT_UNKNOWN_CLIENT", "未知的业务系统")
		}
		return nil, err
	}
	if client.Status != "active" || len(client.SecretEnc) == 0 {
		return nil, httpx.New("AGENT_SECRET_DISABLED", "该业务系统的密钥已停用")
	}

	rawTimestamp := strings.TrimSpace(c.GetHeader(HeaderTimestamp))
	timestamp, parseErr := strconv.ParseInt(rawTimestamp, 10, 64)
	if parseErr != nil {
		return nil, httpx.New("AGENT_TIMESTAMP_SKEW", "时间戳无效")
	}
	now := s.now().Unix()
	if timestamp-now > int64(s.window.Seconds()) || now-timestamp > int64(s.window.Seconds()) {
		return nil, httpx.New("AGENT_TIMESTAMP_SKEW", "时间戳超出容忍窗口")
	}

	nonce := strings.TrimSpace(c.GetHeader(HeaderNonce))
	provided := strings.TrimSpace(c.GetHeader(HeaderSignature))
	if nonce == "" || provided == "" {
		return nil, httpx.New("AGENT_SIGNATURE_INVALID", "签名校验失败")
	}
	secret, err := s.clients.DecryptSecret(client)
	if err != nil {
		return nil, httpx.New("AGENT_SECRET_DISABLED", "该业务系统的密钥不可用")
	}

	canonical := Canonical(
		c.Request.Method,
		c.Request.URL.Path,
		c.Request.URL.Query().Encode(),
		SHA256Hex(body),
		timestamp,
		nonce,
	)
	expected := Signature([]byte(secret), canonical)
	decoded, decodeErr := base64.StdEncoding.DecodeString(provided)
	if decodeErr != nil {
		return nil, httpx.New("AGENT_SIGNATURE_INVALID", "签名校验失败")
	}
	expectedRaw, _ := base64.StdEncoding.DecodeString(expected)
	if !hmac.Equal(expectedRaw, decoded) {
		return nil, httpx.New("AGENT_SIGNATURE_INVALID", "签名校验失败")
	}

	if err := s.consumeNonce(c, clientID, nonce); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Service) consumeNonce(c *gin.Context, clientID, nonce string) error {
	if s.redis == nil {
		return httpx.New("REDIS_UNAVAILABLE", "Redis 不可用")
	}
	key := fmt.Sprintf("%s%s:%s", nonceKeyPrefix, clientID, nonce)
	ok, err := s.redis.Client().SetNX(c.Request.Context(), key, "1", s.window).Result()
	if err != nil {
		return httpx.New("REDIS_UNAVAILABLE", "Redis 不可用")
	}
	if !ok {
		return httpx.New("AGENT_NONCE_REPLAY", "请求重放")
	}
	return nil
}

func readBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
	if err != nil {
		return nil, httpx.New("INVALID_REQUEST", "无法读取请求体")
	}
	if len(raw) > maxBodyBytes {
		return nil, httpx.New("INVALID_REQUEST", "请求体过大")
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}
