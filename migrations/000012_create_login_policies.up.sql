-- 000012 login_policies —— 档位门槛项取值 + 按 client 覆盖

CREATE TABLE login_policies (
    id           uuid        PRIMARY KEY,
    scope        text        NOT NULL
                             CHECK (scope IN ('tier_default','client_override')),
    tier         text        CHECK (tier IN ('L1','L2','L3')),          -- scope=tier_default 必填
    client_id    text        REFERENCES clients(client_id) ON DELETE CASCADE, -- scope=client_override 必填

    -- A · 凭据要求
    min_acr                     text     NOT NULL DEFAULT 'AAL1'
                                         CHECK (min_acr IN ('AAL1','AAL2','AAL3')),
    password_min_length         smallint NOT NULL DEFAULT 12 CHECK (password_min_length BETWEEN 8 AND 128),
    password_char_classes       smallint NOT NULL DEFAULT 3  CHECK (password_char_classes BETWEEN 2 AND 4),
    password_history_count      smallint NOT NULL DEFAULT 5  CHECK (password_history_count >= 0),
    password_max_age_days       integer  NOT NULL DEFAULT 90 CHECK (password_max_age_days > 0),
    -- B · 会话与令牌
    session_idle_timeout_min    integer  NOT NULL DEFAULT 30 CHECK (session_idle_timeout_min > 0),
    session_absolute_timeout_min integer NOT NULL DEFAULT 720 CHECK (session_absolute_timeout_min > 0),
    access_token_ttl_sec        integer  NOT NULL DEFAULT 600 CHECK (access_token_ttl_sec > 0),
    refresh_token_ttl_sec       integer  NOT NULL DEFAULT 28800 CHECK (refresh_token_ttl_sec > 0),
    max_concurrent_sessions     integer  CHECK (max_concurrent_sessions IS NULL OR max_concurrent_sessions > 0), -- NULL=不限
    concurrent_overflow_action  text     NOT NULL DEFAULT 'deny_new'
                                         CHECK (concurrent_overflow_action IN ('deny_new','evict_oldest','evict_oldest_alert')),
    -- C · 失败处理
    fail_threshold              smallint NOT NULL DEFAULT 5 CHECK (fail_threshold > 0),
    lock_duration_min           integer  NOT NULL DEFAULT 15 CHECK (lock_duration_min > 0),
    -- D · 准入与生命周期
    self_registration_allowed   boolean  NOT NULL DEFAULT true,
    password_self_recovery      boolean  NOT NULL DEFAULT true,
    sso_logout_scope            text     NOT NULL DEFAULT 'all_clients'
                                         CHECK (sso_logout_scope IN ('current_client','all_clients')),
    -- E · 因子限制与配套
    disabled_factors            text[]   NOT NULL DEFAULT '{}',
    operator_requirements       jsonb    NOT NULL DEFAULT '{}'::jsonb,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT login_policies_scope_xor CHECK (
        (scope = 'tier_default'    AND tier IS NOT NULL AND client_id IS NULL) OR
        (scope = 'client_override' AND client_id IS NOT NULL AND tier IS NULL)
    ),
    -- 硬约束：刷新令牌有效期 ≤ 会话绝对超时（否则绝对超时被绕过）
    CONSTRAINT login_policies_refresh_le_absolute
        CHECK (refresh_token_ttl_sec <= session_absolute_timeout_min * 60)
);

-- 每个档位仅一份默认
CREATE UNIQUE INDEX login_policies_tier_default_uidx
    ON login_policies (tier) WHERE scope = 'tier_default';
-- 每个客户端仅一份覆盖
CREATE UNIQUE INDEX login_policies_client_override_uidx
    ON login_policies (client_id) WHERE scope = 'client_override';

CREATE TRIGGER login_policies_set_updated_at BEFORE UPDATE ON login_policies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();