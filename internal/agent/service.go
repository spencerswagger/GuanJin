package agent

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store"
)

const (
	HeaderClient    = "X-GuanJin-Client"
	HeaderTimestamp = "X-GuanJin-Timestamp"
	HeaderNonce     = "X-GuanJin-Nonce"
	HeaderSignature = "X-GuanJin-Signature"

	DefaultWindow = 300 * time.Second
	maxBodyBytes  = 4 << 20
)

type Deps struct {
	DB        *gorm.DB
	Clients   *store.ClientRepo
	Redis     *store.Redis
	Snapshots *store.PolicySnapshotRepo
	Bindings  *store.RoleBindingRepo
	MasterKey []byte
	Window    time.Duration
	Now       func() time.Time
}

type Service struct {
	db        *gorm.DB
	clients   *store.ClientRepo
	redis     *store.Redis
	snapshots *store.PolicySnapshotRepo
	bindings  *store.RoleBindingRepo
	masterKey []byte
	window    time.Duration
	now       func() time.Time
}

func New(deps Deps) *Service {
	window := deps.Window
	if window <= 0 {
		window = DefaultWindow
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		db:        deps.DB,
		clients:   deps.Clients,
		redis:     deps.Redis,
		snapshots: deps.Snapshots,
		bindings:  deps.Bindings,
		masterKey: deps.MasterKey,
		window:    window,
		now:       now,
	}
}

func (s *Service) RegisterRoutes(engine *gin.Engine) {
	engine.POST("/api/v1/agent/report", s.Report)
	engine.GET("/api/v1/agent/policies", s.Policies)
}
