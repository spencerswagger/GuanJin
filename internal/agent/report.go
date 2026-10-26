package agent

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type RolePermissions struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type InterfaceMapEntry struct {
	Path     string `json:"path"`
	Method   string `json:"method"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type DefaultRole struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type LabelDef struct {
	Resource string `json:"resource"`
	Level    int32  `json:"level"`
}

type ReportRequest struct {
	ClientID        string              `json:"client_id"`
	SDKVersion      string              `json:"sdk_version"`
	Version         int32               `json:"version"`
	ModelConfDigest string              `json:"model_conf_digest"`
	Permissions     []string            `json:"permissions"`
	Roles           []RolePermissions   `json:"roles"`
	InterfaceMap    []InterfaceMapEntry `json:"interface_map"`
	DefaultRole     DefaultRole         `json:"default_role"`
	LabelDefs       []LabelDef          `json:"label_defs"`
}

func policyIntegrityKey(masterKey []byte) []byte {
	return crypto.Derive(masterKey, "guanjin/hmac/policy-snapshot", 32)
}

func (s *Service) Report(c *gin.Context) {
	body, err := s.authenticate(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if len(body) == 0 {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求体为空"))
		return
	}

	var report ReportRequest
	if err := json.Unmarshal(body, &report); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求体不是合法 JSON"))
		return
	}
	headerClient := c.GetHeader(HeaderClient)
	if report.ClientID == "" {
		report.ClientID = headerClient
	}
	if report.ClientID != headerClient {
		httpx.Error(c, httpx.New("AGENT_UNKNOWN_CLIENT", "client_id 与签名主体不一致"))
		return
	}
	if report.Version <= 0 {
		httpx.Error(c, httpx.New("INVALID_PARAMETER", "version 必须为正整数"))
		return
	}

	canonicalPayload, err := canonicalPayload(body)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求体不是合法 JSON"))
		return
	}
	payloadHash := crypto.Sum(canonicalPayload)
	integrityTag := crypto.HMACSM3(policyIntegrityKey(s.masterKey), payloadHash)
	now := s.now()

	snapshot := &model.PolicySnapshot{
		ClientID:     report.ClientID,
		Version:      report.Version,
		Payload:      canonicalPayload,
		PayloadHash:  payloadHash,
		IntegrityTag: integrityTag,
		ReportedAt:   now,
	}
	if err := s.snapshots.Create(c.Request.Context(), snapshot); err != nil {
		httpx.Error(c, err)
		return
	}

	if report.DefaultRole.Role != "" {
		if err := s.db.WithContext(c.Request.Context()).Model(&model.Client{}).
			Where("client_id = ?", report.ClientID).
			Update("default_role", report.DefaultRole.Role).Error; err != nil {
			httpx.Error(c, err)
			return
		}
	}

	c.JSON(200, gin.H{
		"snapshot_id":    snapshot.ID.String(),
		"stored_version": report.Version,
	})
}

func canonicalPayload(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
