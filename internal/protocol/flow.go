package protocol

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/session"
)

type submitAnswer struct {
	Factor      string `json:"factor"`
	ChallengeID string `json:"challenge_id"`
	Response    string `json:"response"`
	Username    string `json:"username,omitempty"`
}

type submitRequest struct {
	FlowID  string         `json:"flow_id"`
	Answers []submitAnswer `json:"answers"`
}

type challengeRequest struct {
	FlowID   string `json:"flow_id" form:"flow_id"`
	Purpose  string `json:"purpose" form:"purpose"`
	Target   string `json:"target" form:"target"`
	UserHint string `json:"user_hint" form:"user_hint"`
}

type verifyRequest struct {
	ChallengeID string `json:"challenge_id" form:"challenge_id"`
	Response    string `json:"response" form:"response"`
	FlowID      string `json:"flow_id" form:"flow_id"`
}

func (s *Server) AuthorizeFlow(c *gin.Context) {
	state, err := s.loadFlow(c, c.Query("flow_id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	disabled := s.disabledForClient(c, state.ClientID, state.Tier)
	c.JSON(http.StatusOK, s.orch.Descriptor(state, disabled, s.regMode(c, state)))
}

func (s *Server) AuthorizeSubmit(c *gin.Context) {
	req, err := parseSubmit(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	s.processSubmit(c, req)
}

func (s *Server) processSubmit(c *gin.Context, req *submitRequest) {
	ctx := c.Request.Context()
	state, err := s.loadFlow(c, req.FlowID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	disabled := s.disabledForClient(c, state.ClientID, state.Tier)

	for _, ans := range req.Answers {
		typ := ans.Factor
		if typ == "" {
			if p, _, ok := s.orch.NextStep(state.Achieved, disabled); ok {
				typ = p.Type()
			}
		}
		if typ == "" {
			httpx.Error(c, factor.ErrNoEligibleFactor)
			return
		}
		if state.HasFactor(typ) {
			continue
		}
		p, ok := s.registry.Get(typ)
		if !ok {
			httpx.Error(c, factor.ErrFactorNotSupported)
			return
		}
		if !s.registry.Enabled(typ) || containsString(disabled, typ) {
			httpx.Error(c, factor.ErrFactorDisabled)
			return
		}
		authenticatable, ok := p.(factor.Authenticatable)
		if !ok {
			httpx.Error(c, factor.ErrNoEligibleFactor)
			return
		}
		ticket := factor.Ticket{ChallengeID: ans.ChallengeID, FactorType: typ}
		if ticket.ChallengeID == "" {
			userID := state.UserID
			if userID == "" && ans.Username != "" {
				user, userErr := s.users.GetByUsername(ctx, ans.Username)
				if userErr != nil {
					httpx.Error(c, factor.ErrInvalidCredential)
					return
				}
				userID = user.ID.String()
			}
			if userID == "" {
				httpx.Error(c, httpx.New("INVALID_REQUEST", "缺少用户上下文"))
				return
			}
			begun, beginErr := authenticatable.BeginChallenge(ctx, userID, factor.PurposeLogin)
			if beginErr != nil {
				httpx.Error(c, beginErr)
				return
			}
			ticket = begun
			state.UserID = userID
		}
		if verifyErr := authenticatable.VerifyResponse(ctx, ticket, ans.Response); verifyErr != nil {
			httpx.Error(c, verifyErr)
			return
		}
		state.AddFactor(typ)
	}

	state.CurrentACR = s.orch.CurrentACR(state.Achieved, disabled)
	state.Satisfied = s.orch.Satisfied(state.CurrentACR, state.TargetACR)
	if err := s.flows.Put(ctx, state); err != nil {
		httpx.Error(c, err)
		return
	}

	if state.Satisfied {
		s.completeFlow(c, state, disabled)
		return
	}
	if _, _, ok := s.orch.NextStep(state.Achieved, disabled); !ok {
		desc := s.orch.Descriptor(state, disabled, s.regMode(c, state))
		httpx.Error(c, httpx.New("ACR_INSUFFICIENT", "当前可用认证方式无法达到所需保证级别").
			WithDetails(map[string]any{"flow": desc}))
		return
	}
	desc := s.orch.Descriptor(state, disabled, s.regMode(c, state))
	c.JSON(http.StatusOK, gin.H{"status": "need_more", "flow": desc})
}

func (s *Server) completeFlow(c *gin.Context, state *flow.State, disabled []string) {
	ctx := c.Request.Context()
	userID, err := uuid.Parse(state.UserID)
	if err != nil {
		httpx.Error(c, &httpx.AppError{Code: "INTERNAL_ERROR", HTTP: http.StatusInternalServerError, Message: httpx.MessageFor("INTERNAL_ERROR")})
		return
	}
	policy, err := s.policies.GetEffective(ctx, state.ClientID, state.Tier)
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
		UserID:    userID,
		ClientID:  state.ClientID,
		ACR:       state.CurrentACR,
		AMR:       s.orch.AMR(state.Achieved, disabled),
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

	code, err := s.issueAuthCodeForSession(c, sess, state.ClientID, state.RedirectURI, state.Nonce, state.Scope, state.CodeChallenge)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	_ = s.flows.Delete(ctx, state.FlowID)
	c.JSON(http.StatusOK, gin.H{
		"status":       "completed",
		"code":         code,
		"redirect_uri": state.RedirectURI,
		"state":        state.State,
	})
}

func (s *Server) Challenge(c *gin.Context) {
	ctx := c.Request.Context()
	typ := c.Param("factor")
	p, ok := s.registry.Get(typ)
	if !ok {
		httpx.Error(c, factor.ErrFactorNotSupported)
		return
	}
	if !s.registry.Enabled(typ) {
		httpx.Error(c, factor.ErrFactorDisabled)
		return
	}
	authenticatable, ok := p.(factor.Authenticatable)
	if !ok {
		httpx.Error(c, factor.ErrNoEligibleFactor)
		return
	}
	var req challengeRequest
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}

	var state *flow.State
	if req.FlowID != "" {
		loaded, err := s.loadFlow(c, req.FlowID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		state = loaded
		if containsString(s.disabledForClient(c, state.ClientID, state.Tier), typ) {
			httpx.Error(c, factor.ErrFactorDisabled)
			return
		}
	}

	userID := ""
	if state != nil {
		userID = state.UserID
	}
	if userID == "" && req.UserHint != "" {
		if user, err := s.users.GetByUsername(ctx, req.UserHint); err == nil {
			userID = user.ID.String()
		}
	}
	if userID == "" {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "缺少用户上下文"))
		return
	}

	ticket, err := authenticatable.BeginChallenge(ctx, userID, factor.ParsePurpose(req.Purpose))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if state != nil && state.UserID == "" {
		state.UserID = userID
		_ = s.flows.Put(ctx, state)
	}
	c.JSON(http.StatusOK, gin.H{
		"challenge_id":  ticket.ChallengeID,
		"ttl":           int(ticket.TTL.Seconds()),
		"attempts_left": ticket.AttemptsLeft,
		"hint":          gin.H{"target_masked": ticket.TargetMasked},
		"next_action":   string(ticket.NextAction),
	})
}

func (s *Server) Verify(c *gin.Context) {
	ctx := c.Request.Context()
	typ := c.Param("factor")
	p, ok := s.registry.Get(typ)
	if !ok {
		httpx.Error(c, factor.ErrFactorNotSupported)
		return
	}
	authenticatable, ok := p.(factor.Authenticatable)
	if !ok {
		httpx.Error(c, factor.ErrNoEligibleFactor)
		return
	}
	var req verifyRequest
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	ticket := factor.Ticket{ChallengeID: req.ChallengeID, FactorType: typ}
	if err := authenticatable.VerifyResponse(ctx, ticket, req.Response); err != nil {
		httpx.Error(c, err)
		return
	}

	resp := gin.H{"verified": true}
	if req.FlowID != "" {
		state, err := s.loadFlow(c, req.FlowID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		if cid, err := uuid.Parse(req.ChallengeID); err == nil {
			if ch, chErr := s.challenges.GetByID(ctx, cid); chErr == nil && ch.UserID != nil {
				state.UserID = ch.UserID.String()
			}
		}
		state.AddFactor(typ)
		disabled := s.disabledForClient(c, state.ClientID, state.Tier)
		state.CurrentACR = s.orch.CurrentACR(state.Achieved, disabled)
		state.Satisfied = s.orch.Satisfied(state.CurrentACR, state.TargetACR)
		if err := s.flows.Put(ctx, state); err != nil {
			httpx.Error(c, err)
			return
		}
		resp["acr"] = state.CurrentACR
		resp["amr"] = s.orch.AMR(state.Achieved, disabled)
	} else {
		resp["amr"] = []string{s.amrForType(typ)}
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) regMode(c *gin.Context, state *flow.State) string {
	policy, err := s.policies.GetEffective(c.Request.Context(), state.ClientID, state.Tier)
	if err != nil {
		return "self"
	}
	return registrationMode(policy)
}

func parseSubmit(c *gin.Context) (*submitRequest, error) {
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		var req submitRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			return nil, httpx.New("INVALID_REQUEST", "请求体无效")
		}
		return &req, nil
	}
	if err := c.Request.ParseForm(); err != nil {
		return nil, httpx.New("INVALID_REQUEST", "无法解析表单")
	}
	req := &submitRequest{FlowID: c.PostForm("flow_id")}
	answer := submitAnswer{
		Factor:      c.PostForm("factor"),
		ChallengeID: c.PostForm("challenge_id"),
		Response:    c.PostForm("response"),
		Username:    c.PostForm("username"),
	}
	if answer.Factor != "" || answer.Response != "" || answer.ChallengeID != "" {
		req.Answers = append(req.Answers, answer)
	}
	return req, nil
}
