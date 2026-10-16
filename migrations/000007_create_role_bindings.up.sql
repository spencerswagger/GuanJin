-- 000007 role_bindings —— 用户 → 角色绑定

CREATE TABLE role_bindings (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id     text        NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
    role          text        NOT NULL,                      -- 业务系统定义的角色名
    status        text        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','revoked')),
    granted_by    uuid        REFERENCES users(id) ON DELETE SET NULL,  -- 安全管理员
    granted_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    revoke_reason text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT role_bindings_uniq UNIQUE (user_id, client_id, role),
    CONSTRAINT role_bindings_revoked_consistency
        CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE INDEX role_bindings_user_active_idx ON role_bindings (user_id) WHERE status = 'active';
CREATE INDEX role_bindings_client_role_idx ON role_bindings (client_id, role);

CREATE TRIGGER role_bindings_set_updated_at BEFORE UPDATE ON role_bindings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();