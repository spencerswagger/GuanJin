package agent

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/httpx"
)

type Binding struct {
	UserID string   `json:"user_id"`
	Roles  []string `json:"roles"`
}

type PoliciesResponse struct {
	Version  int32     `json:"version"`
	Bindings []Binding `json:"bindings"`
	IssuedAt string    `json:"issued_at"`
}

func (s *Service) Policies(c *gin.Context) {
	body, err := s.authenticate(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if len(body) > 0 {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "该端点不接受请求体"))
		return
	}
	clientID := c.GetHeader(HeaderClient)

	var userIDs []uuid.UUID
	for _, raw := range append(c.QueryArray("user_ids[]"), c.QueryArray("user_ids")...) {
		if id, err := uuid.Parse(raw); err == nil {
			userIDs = append(userIDs, id)
		}
	}

	version, err := s.snapshots.MaxVersionByClient(c.Request.Context(), clientID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	grouped, err := s.bindings.ActiveGroupedByUser(c.Request.Context(), clientID, userIDs)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	bindings := make([]Binding, 0, len(grouped))
	if len(userIDs) > 0 {
		for _, userID := range userIDs {
			roles := grouped[userID]
			if roles == nil {
				roles = []string{}
			}
			bindings = append(bindings, Binding{UserID: userID.String(), Roles: roles})
		}
	} else {
		for userID, roles := range grouped {
			bindings = append(bindings, Binding{UserID: userID.String(), Roles: roles})
		}
	}

	c.JSON(200, PoliciesResponse{
		Version:  version,
		Bindings: bindings,
		IssuedAt: s.now().UTC().Format(time.RFC3339),
	})
}
