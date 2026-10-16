-- 000004 authenticators —— 能证明"我是我"的凭据

CREATE TABLE authenticators (
    id               uuid        PRIMARY KEY,
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- 可空：channel 型因子不建认证器；holding 型通常也无关联标识符
    identifier_id    uuid        REFERENCES identifiers(id) ON DELETE SET NULL,
    -- 开放集合：password / totp / recovery_code / webauthn ...（不加 CHECK）
    type             text        NOT NULL,
    -- 凭据信封（结构由因子实现自描述，DB 不解释）
    credential_data  jsonb       NOT NULL,
    status           text        NOT NULL DEFAULT 'active'
                                 CHECK (status IN ('active','disabled','revoked')),
    label            text,                                    -- 展示名，如"我的 iPhone"
    fail_count       integer     NOT NULL DEFAULT 0 CHECK (fail_count >= 0),
    locked_until     timestamptz,
    last_used_at     timestamptz,
    expires_at       timestamptz,                             -- 恢复码/临时凭据可设过期
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT authenticators_fail_nonneg CHECK (fail_count >= 0)
);

CREATE INDEX authenticators_user_idx ON authenticators (user_id);
CREATE INDEX authenticators_user_type_active_idx
    ON authenticators (user_id, type) WHERE status = 'active';

CREATE TRIGGER authenticators_set_updated_at BEFORE UPDATE ON authenticators
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();