-- 000005 sessions —— 会话

CREATE TABLE sessions (
    id              uuid        PRIMARY KEY,
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- 会话归属的业务系统（登录一定有客户端上下文；管理后台本身也是一个 client）
    client_id       text        NOT NULL REFERENCES clients(client_id) ON DELETE RESTRICT,
    acr             text        NOT NULL CHECK (acr IN ('AAL1','AAL2','AAL3')),
    amr             text[]      NOT NULL DEFAULT '{}',       -- ['pwd','totp',...]
    status          text        NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','revoked','expired')),
    issued_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz,
    expires_at      timestamptz NOT NULL,                    -- 绝对超时（重认证间隔）
    idle_expires_at timestamptz NOT NULL,                    -- 空闲超时（可滚动）
    revoked_at      timestamptz,
    revoke_reason   text,                                    -- logout|logout_all|admin|risk|role_change|password_change
    -- 设备上下文
    ip              inet,
    user_agent      text,
    device_id       text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sessions_revoked_consistency
        CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
    CONSTRAINT sessions_time_order
        CHECK (idle_expires_at <= expires_at)
);

-- 撤销/单点登出：按用户批量失效
CREATE INDEX sessions_user_status_idx ON sessions (user_id, status);
-- 并发会话数统计（按 client）与"挤掉最旧"
CREATE INDEX sessions_user_client_active_idx
    ON sessions (user_id, client_id, issued_at DESC) WHERE status = 'active';
-- 空闲超时清理
CREATE INDEX sessions_idle_idx ON sessions (idle_expires_at) WHERE status = 'active';

CREATE TRIGGER sessions_set_updated_at BEFORE UPDATE ON sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();