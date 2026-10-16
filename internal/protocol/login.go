package protocol

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
)

var atomByFactor = map[string]flow.UiAtom{
	"password":      flow.AtomPassword,
	"sms_otp":       flow.AtomOTP,
	"email_otp":     flow.AtomOTP,
	"totp":          flow.AtomOTP,
	"recovery_code": flow.AtomOTP,
	"qrcode":        flow.AtomQrCode,
}

const fallbackAtom = flow.AtomOTP

func resolveAtom(factor string) flow.UiAtom {
	if atom, ok := atomByFactor[factor]; ok {
		return atom
	}
	return fallbackAtom
}

var flowLoginTemplate = template.Must(template.New("login-flow").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>关津统一登录</title></head>
<body>
<h1>{{.ClientName}}</h1>
<p>当前保证级别 {{.CurrentACR}} → 需要 {{.TargetACR}}</p>
<ol>
{{range .Steps}}
  <li data-factor="{{.Factor}}" data-status="{{.Status}}">{{.Factor}}{{if .Done}} 已通过 ✓{{end}}</li>
{{end}}
</ol>
{{if .Active}}
<form method="post" action="/authorize/submit">
  <input type="hidden" name="flow_id" value="{{.FlowID}}">
  <input type="hidden" name="factor" value="{{.Active.Factor}}">
  {{if eq .Active.Atom "password"}}
  <label>用户名 <input type="text" name="username" autocomplete="username"></label><br>
  <label>口令 <input type="password" name="response" autocomplete="current-password"></label><br>
  {{else if eq .Active.Atom "otp"}}
  <label>验证码 <input type="text" name="response" inputmode="numeric" autocomplete="one-time-code"></label><br>
  {{else if eq .Active.Atom "qrcode"}}
  <p>请扫码完成验证（预留）</p>
  {{else}}
  <label>凭据 <input type="text" name="response"></label><br>
  {{end}}
  <button type="submit">继续</button>
</form>
{{else}}
<p>已完成认证，正在跳转…</p>
{{end}}
</body>
</html>`))

var loginNoticeTemplate = template.Must(template.New("login-notice").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>关津统一登录</title></head>
<body>
<h1>关津统一登录</h1>
<p class="notice">{{.Message}}</p>
</body>
</html>`))

type loginStepView struct {
	Factor string
	Role   string
	Status string
	Atom   flow.UiAtom
	Done   bool
}

type loginView struct {
	FlowID     string
	ClientName string
	CurrentACR string
	TargetACR  string
	Steps      []loginStepView
	Active     *loginStepView
}

func buildLoginView(desc *flow.FlowDescriptor) *loginView {
	view := &loginView{
		FlowID:     desc.FlowID,
		ClientName: desc.Client.Name,
		CurrentACR: desc.CurrentACR,
		TargetACR:  desc.TargetACR,
	}
	for _, step := range desc.Steps {
		item := loginStepView{
			Factor: step.Factor,
			Role:   step.Role,
			Status: step.Status,
			Atom:   resolveAtom(step.Factor),
			Done:   step.Status == flow.StatusDone,
		}
		view.Steps = append(view.Steps, item)
		if view.Active == nil && step.Status == flow.StatusPending {
			active := item
			view.Active = &active
		}
	}
	return view
}

func (s *Server) LoginPage(c *gin.Context) {
	flowID := c.Query("flow_id")
	if flowID == "" {
		s.renderLoginNotice(c, "登录会话已过期，请重新从业务系统发起登录")
		return
	}
	state, err := s.loadFlow(c, flowID)
	if err != nil {
		s.renderLoginNotice(c, "登录会话已过期，请重新从业务系统发起登录")
		return
	}
	disabled := s.disabledForClient(c, state.ClientID, state.Tier)
	desc := s.orch.Descriptor(state, disabled, s.regMode(c, state))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	_ = flowLoginTemplate.Execute(c.Writer, buildLoginView(desc))
}

func (s *Server) renderLoginNotice(c *gin.Context, message string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	_ = loginNoticeTemplate.Execute(c.Writer, gin.H{"Message": message})
}

func (s *Server) Login(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "无法解析表单"))
		return
	}
	req := &submitRequest{FlowID: c.PostForm("flow_id")}
	req.Answers = append(req.Answers, submitAnswer{
		Factor:      c.PostForm("factor"),
		ChallengeID: c.PostForm("challenge_id"),
		Response:    c.PostForm("response"),
		Username:    c.PostForm("username"),
	})
	s.processSubmit(c, req)
}
