package protocol

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store"
)

func (s *Server) JWKS(c *gin.Context) {
	ctx := c.Request.Context()
	doc := crypto.JWKSDocument{Keys: []crypto.JWK{}}

	for _, purpose := range []string{crypto.PurposeOIDC, crypto.PurposeGateway} {
		rows, err := s.keys.ListPublishable(ctx, purpose)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		for _, row := range rows {
			pub, err := store.PublicKeyFromText(row.PublicKey)
			if err != nil {
				continue
			}
			doc.Keys = append(doc.Keys, crypto.NewJWK(row.Kid, row.Algorithm, pub))
		}
	}

	c.JSON(http.StatusOK, doc)
}
