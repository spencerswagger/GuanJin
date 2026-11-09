package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是适配层的**纯单测**（无数据库）：只钉"哨兵翻译"与"字段搬运"。
//
// 为什么要单测这两件事：它们是把 `store` 的事实变成网关语义的唯一阀门——
// `store.ErrPolicyNotConfigured` 少翻一行，对外就从 500（配置缺陷）变成 503（依赖挂了），
// 运维会照着码表去查库，而问题在配置。真库 roundtrip 由 `adapter_db_test.go` 负责。

const testUID = "0193f0aa-0000-7000-8000-000000000001"

type stubSessionRepo struct {
	row *model.Session
	err error
}

func (s stubSessionRepo) GetByID(context.Context, uuid.UUID) (*model.Session, error) {
	return s.row, s.err
}

type stubPolicyRepo struct {
	policy *store.AdmissionPolicy
	err    error
}

func (s stubPolicyRepo) LoadAdmissionPolicy(context.Context, string) (*store.AdmissionPolicy, error) {
	return s.policy, s.err
}

type stubRoleRepo struct {
	roles []string
	err   error
}

func (s stubRoleRepo) RolesForUserClient(context.Context, uuid.UUID, string) ([]string, error) {
	return s.roles, s.err
}

type stubUserRepo struct {
	user *model.User
	err  error
}

func (s stubUserRepo) GetByID(context.Context, uuid.UUID) (*model.User, error) { return s.user, s.err }

type stubVisitRepo struct {
	userID   uuid.UUID
	clientID string
	tier     string
	at       time.Time
	err      error
}

func (s *stubVisitRepo) RecordVisit(_ context.Context, userID uuid.UUID, clientID, tier string, at time.Time) error {
	s.userID, s.clientID, s.tier, s.at = userID, clientID, tier, at
	return s.err
}

type stubFlowRepo struct {
	flow *model.Flow
	err  error
}

func (s stubFlowRepo) FindActiveByUserClient(context.Context, uuid.UUID, string) (*model.Flow, error) {
	return s.flow, s.err
}

type stubStatusRepo struct {
	status string
	err    error
}

func (s stubStatusRepo) SessionStatus(context.Context, string) (string, error) {
	return s.status, s.err
}

func TestLoadSessionMapsSnapshotAndSentinels(t *testing.T) {
	sid := uuid.MustParse("0193f0aa-0000-7000-8000-000000000002")
	now := time.Now().UTC()
	row := &model.Session{
		UserID: uuid.MustParse(testUID), ClientID: "billing", ACR: "AAL2",
		AMR: model.StringArray{"pwd", "otp"}, Status: model.SessionStatusActive,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	row.ID = sid

	adapter := NewSessionsWithRepo(stubSessionRepo{row: row})
	got, err := adapter.LoadSession(context.Background(), sid.String())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.ID != sid.String() || got.UserID != testUID || got.ClientID != "billing" || got.ACR != "AAL2" {
		t.Fatalf("字段搬运不符：%+v", got)
	}
	if len(got.AMR) != 2 || got.AMR[1] != "otp" {
		t.Fatalf("amr 搬运不符：%v", got.AMR)
	}

	// 哨兵翻译：`store.ErrSessionNotFound` → `gateway.ErrSessionNotFound`。
	missing := NewSessionsWithRepo(stubSessionRepo{err: store.ErrSessionNotFound})
	if _, err := missing.LoadSession(context.Background(), sid.String()); !errors.Is(err, gateway.ErrSessionNotFound) {
		t.Fatalf("期望 gateway.ErrSessionNotFound，实际 %v", err)
	}
	// 非法 sid：不解析、不查库，直接按"不存在"处理。
	if _, err := missing.LoadSession(context.Background(), "not-a-uuid"); !errors.Is(err, gateway.ErrSessionNotFound) {
		t.Fatalf("非法 sid 期望 gateway.ErrSessionNotFound，实际 %v", err)
	}
	// 其他错误原样上抛（让 gateway 归类成 503），**不许**吞成"不存在"。
	boom := errors.New("connection refused")
	if _, err := NewSessionsWithRepo(stubSessionRepo{err: boom}).LoadSession(context.Background(), sid.String()); !errors.Is(err, boom) {
		t.Fatalf("期望原样上抛 %v，实际 %v", boom, err)
	}
}

func TestLoadAdmissionPolicyTranslatesAllSentinels(t *testing.T) {
	cases := []struct {
		name   string
		in     error
		wantIn error
	}{
		{"业务系统不存在", store.ErrClientNotFound, gateway.ErrClientNotFound},
		{"业务系统已停用", store.ErrClientDisabled, gateway.ErrClientDisabled},
		{"两侧策略都没有", store.ErrPolicyNotConfigured, gateway.ErrPolicyNotConfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewPoliciesWithRepo(stubPolicyRepo{err: tc.in})
			_, err := adapter.LoadAdmissionPolicy(context.Background(), "billing")
			if !errors.Is(err, tc.wantIn) {
				t.Fatalf("期望 %v，实际 %v", tc.wantIn, err)
			}
		})
	}

	// 其他错误不翻译（保持 503 的判定依据）。
	boom := errors.New("connection refused")
	if _, err := NewPoliciesWithRepo(stubPolicyRepo{err: boom}).LoadAdmissionPolicy(context.Background(), "billing"); !errors.Is(err, boom) {
		t.Fatalf("期望原样上抛，实际 %v", err)
	}

	// 正常路径：字段搬运 + 禁用因子复制（不共享底层数组）。
	disabled := []string{"otp"}
	adapter := NewPoliciesWithRepo(stubPolicyRepo{policy: &store.AdmissionPolicy{
		ClientID: "billing", ClientName: "计费", Tier: "L3", MinACR: "AAL2", DisabledFactors: disabled,
	}})
	got, err := adapter.LoadAdmissionPolicy(context.Background(), "billing")
	if err != nil {
		t.Fatalf("LoadAdmissionPolicy: %v", err)
	}
	if got.MinACR != "AAL2" || got.Tier != "L3" || got.ClientName != "计费" {
		t.Fatalf("字段不符：%+v", got)
	}
	disabled[0] = "mutated"
	if got.DisabledFactors[0] != "otp" {
		t.Fatalf("DisabledFactors 与入参共享了底层数组：%v", got.DisabledFactors)
	}
}

func TestRolesAndUsersAndVisitsAdapters(t *testing.T) {
	// 角色：正常搬运 + 非法 user id 报错（不静默返回空集，否则用户会"悄悄没有角色"）。
	roles, err := NewRolesWithRepo(stubRoleRepo{roles: []string{"admin"}}).
		RolesForUserClient(context.Background(), testUID, "billing")
	if err != nil || len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("roles 不符：%v / %v", roles, err)
	}
	if _, err := NewRolesWithRepo(stubRoleRepo{}).
		RolesForUserClient(context.Background(), "bad-uuid", "billing"); err == nil {
		t.Fatal("非法 user id 必须报错")
	}

	// 用户名：取 `username`（不是 display_name）。
	user := &model.User{Username: "alice"}
	user.ID = uuid.MustParse(testUID)
	user.DisplayName = strPtr("Alice A.")
	name, err := NewUsersWithRepo(stubUserRepo{user: user}).Username(context.Background(), testUID)
	if err != nil || name != "alice" {
		t.Fatalf("username 不符：%q / %v", name, err)
	}
	if _, err := NewUsersWithRepo(stubUserRepo{}).Username(context.Background(), "bad-uuid"); err == nil {
		t.Fatal("非法 user id 必须报错")
	}

	// 访问记录：user_id 解析后传给仓储（时间由适配器给），错误原样上抛。
	repo := &stubVisitRepo{}
	adapterVisits := NewVisitsWithRepo(repo)
	if err := adapterVisits.RecordVisit(context.Background(), testUID, "billing", "L2"); err != nil {
		t.Fatalf("RecordVisit: %v", err)
	}
	if repo.userID != uuid.MustParse(testUID) || repo.clientID != "billing" || repo.tier != "L2" || repo.at.IsZero() {
		t.Fatalf("访问记录参数不符：%+v", repo)
	}
	if err := adapterVisits.RecordVisit(context.Background(), "bad-uuid", "billing", "L2"); err == nil {
		t.Fatal("非法 user id 必须报错")
	}
}

func TestFindActiveFlowAndSessionStatus(t *testing.T) {
	flowID := uuid.MustParse("0193f0aa-0000-7000-8000-00000000abcd")
	now := time.Now().UTC()
	row := &model.Flow{ClientID: "billing", TargetACR: "AAL2", AchievedAMR: model.StringArray{"pwd"}, ExpiresAt: now}
	row.ID = flowID

	got, err := NewFlowsWithRepo(stubFlowRepo{flow: row}).
		FindActiveFlow(context.Background(), testUID, "billing")
	if err != nil || got == nil || got.ID != flowID.String() || got.TargetACR != "AAL2" {
		t.Fatalf("活跃流不符：%+v / %v", got, err)
	}
	// 没有活跃流 = (nil, nil)，不是错误（档位不足时要照常渲染 need_more）。
	if got, err := NewFlowsWithRepo(stubFlowRepo{err: store.ErrFlowNotFound}).
		FindActiveFlow(context.Background(), testUID, "billing"); err != nil || got != nil {
		t.Fatalf("期望 (nil,nil)，实际 (%v,%v)", got, err)
	}
	boom := errors.New("connection refused")
	if _, err := NewFlowsWithRepo(stubFlowRepo{err: boom}).
		FindActiveFlow(context.Background(), testUID, "billing"); !errors.Is(err, boom) {
		t.Fatalf("期望原样上抛，实际 %v", err)
	}
	if _, err := NewFlowsWithRepo(stubFlowRepo{}).
		FindActiveFlow(context.Background(), "bad-uuid", "billing"); err == nil {
		t.Fatal("非法 user id 必须报错")
	}

	// 会话状态：`store.ErrSessionNotFound` → `protocol.ErrSessionGone`。
	if st, err := NewSessionStatusWithRepo(stubStatusRepo{status: "active"}).
		SessionStatus(context.Background(), "sid"); err != nil || st != "active" {
		t.Fatalf("状态不符：%q / %v", st, err)
	}
	if _, err := NewSessionStatusWithRepo(stubStatusRepo{err: store.ErrSessionNotFound}).
		SessionStatus(context.Background(), "sid"); !errors.Is(err, protocol.ErrSessionGone) {
		t.Fatalf("期望 protocol.ErrSessionGone，实际 %v", err)
	}
	if _, err := NewSessionStatusWithRepo(stubStatusRepo{err: boom}).
		SessionStatus(context.Background(), "sid"); !errors.Is(err, boom) {
		t.Fatalf("期望原样上抛，实际 %v", err)
	}
}

func strPtr(s string) *string { return &s }
