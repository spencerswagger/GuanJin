package protocol

import (
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/session"
)

var loginTemplate = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>关津登录</title></head>
<body>
<h1>关津统一登录</h1>
<form method="post" action="/login">
  <label>用户名 <input type="text" name="username" autocomplete="username"></label><br>
  <label>口令 <input type="password" name="password" autocomplete="current-password"></label><br>
  {{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
  <button type="submit">登录</button>
</form>
</body>
</html>`))

func (s *Server) LoginPage(c *gin.Context) {
	params := authorizationRequestParams(c)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	_ = loginTemplate.Execute(c.Writer, gin.H{"Params": params})
}

func (s *Server) Login(c *gin.Context) {
	ctx := c.Request.Context()
	if err := c.Request.ParseForm(); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "无法解析表单"))
		return
	}

	params := authorizationRequestParams(c)
	clientID := params["client_id"]
	if clientID == "" {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "缺少 client_id"))
		return
	}
	client, err := s.clients.GetByID(ctx, clientID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端不存在"))
		return
	}

	username := c.PostForm("username")
	password := c.PostForm("password")

	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_CREDENTIAL", "用户名或口令错误"))
		return
	}
	if user.Status != "active" {
		httpx.Error(c, httpx.New("ACCOUNT_DISABLED", "账号已停用"))
		return
	}

	auth, err := s.authenticators.GetActiveByUserType(ctx, user.ID, "password")
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_CREDENTIAL", "用户名或口令错误"))
		return
	}

	ok, err := credential.Verify(auth.CredentialData, []byte(password))
	if err != nil || !ok {
		auth.FailCount++
		_ = s.authenticators.Update(ctx, auth)
		httpx.Error(c, httpx.New("INVALID_CREDENTIAL", "用户名或口令错误"))
		return
	}

	now := time.Now().UTC()
	auth.FailCount = 0
	auth.LastUsedAt = &now
	if err := s.authenticators.Update(ctx, auth); err != nil {
		httpx.Error(c, err)
		return
	}

	policy, err := s.policies.GetEffective(ctx, clientID, client.Tier)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	timeouts := session.Timeouts{}
	if policy != nil {
		timeouts.Idle = time.Duration(policy.SessionIdleTimeoutMin) * time.Minute
		timeouts.Absolute = time.Duration(policy.SessionAbsoluteTimeoutMin) * time.Minute
	}

	sess, err := s.sessionMgr.Create(ctx, session.CreateParams{
		UserID:    user.ID,
		ClientID:  clientID,
		ACR:       acrAAL1,
		AMR:       []string{"pwd"},
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
		Timeouts:  timeouts,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}

	maxAge := int(session.DefaultAbsoluteTimeout.Seconds())
	if timeouts.Absolute > 0 {
		maxAge = int(timeouts.Absolute.Seconds())
	}
	s.setSessionCookie(c, sess.ID.String(), maxAge)

	c.Redirect(http.StatusFound, "/authorize?"+queryString(params))
}
