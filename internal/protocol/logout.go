package protocol

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) Logout(c *gin.Context) {
	_ = c.Request.ParseForm()
	ctx := c.Request.Context()

	clientID := c.PostForm("client_id")
	if clientID == "" {
		clientID = c.Query("client_id")
	}
	postLogoutURI := c.PostForm("post_logout_redirect_uri")
	if postLogoutURI == "" {
		postLogoutURI = c.Query("post_logout_redirect_uri")
	}
	state := c.PostForm("state")
	if state == "" {
		state = c.Query("state")
	}

	sess := s.currentSession(c)
	if sess != nil {
		if clientID == "" {
			clientID = sess.ClientID
		}
		tier := ""
		if client, err := s.clients.GetByID(ctx, clientID); err == nil {
			tier = client.Tier
		}
		if tier == "L1" {
			_ = s.sessionMgr.Revoke(ctx, sess.ID, "logout")
		} else {
			_, _ = s.sessionMgr.RevokeAllForUser(ctx, sess.UserID, "logout_all")
		}
	}

	s.clearSessionCookie(c)

	if postLogoutURI != "" {
		c.Redirect(http.StatusFound, withQuery(postLogoutURI, map[string]string{"state": state}))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
