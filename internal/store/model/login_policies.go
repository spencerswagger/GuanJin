package model

// LoginPolicy 映射 login_policies 表（可变表）。
type LoginPolicy struct {
	BaseModel
	Scope                     string   `gorm:"column:scope;not null"`
	Tier                      *string  `gorm:"column:tier"`
	ClientID                  *string  `gorm:"column:client_id"`
	MinACR                    string   `gorm:"column:min_acr;not null;default:AAL1"`
	PasswordMinLength         int16    `gorm:"column:password_min_length;type:smallint;not null;default:12"`
	PasswordCharClasses       int16    `gorm:"column:password_char_classes;type:smallint;not null;default:3"`
	PasswordHistoryCount      int16    `gorm:"column:password_history_count;type:smallint;not null;default:5"`
	PasswordMaxAgeDays        int32    `gorm:"column:password_max_age_days;type:integer;not null;default:90"`
	SessionIdleTimeoutMin     int32    `gorm:"column:session_idle_timeout_min;type:integer;not null;default:30"`
	SessionAbsoluteTimeoutMin int32    `gorm:"column:session_absolute_timeout_min;type:integer;not null;default:720"`
	AccessTokenTTLSec         int32    `gorm:"column:access_token_ttl_sec;type:integer;not null;default:600"`
	RefreshTokenTTLSec        int32    `gorm:"column:refresh_token_ttl_sec;type:integer;not null;default:28800"`
	MaxConcurrentSessions     *int32   `gorm:"column:max_concurrent_sessions;type:integer"`
	ConcurrentOverflowAction  string   `gorm:"column:concurrent_overflow_action;not null;default:deny_new"`
	FailThreshold             int16    `gorm:"column:fail_threshold;type:smallint;not null;default:5"`
	LockDurationMin           int32    `gorm:"column:lock_duration_min;type:integer;not null;default:15"`
	SelfRegistrationAllowed   bool     `gorm:"column:self_registration_allowed;not null;default:true"`
	PasswordSelfRecovery      bool     `gorm:"column:password_self_recovery;not null;default:true"`
	SSOLogoutScope            string   `gorm:"column:sso_logout_scope;not null;default:all_clients"`
	DisabledFactors           []string `gorm:"column:disabled_factors;type:text[];not null"`
	OperatorRequirements      []byte   `gorm:"column:operator_requirements;type:jsonb;not null"`
}

func (LoginPolicy) TableName() string { return "login_policies" }
