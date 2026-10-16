-- 000006 challenges —— 一次性挑战

CREATE TABLE challenges (
    id               uuid        PRIMARY KEY,
    -- 注册场景可能尚无用户 → 可空
    user_id          uuid        REFERENCES users(id) ON DELETE SET NULL,
    identifier_id    uuid        REFERENCES identifiers(id) ON DELETE SET NULL,
    authenticator_id uuid        REFERENCES authenticators(id) ON DELETE SET NULL,
    client_id        text        REFERENCES clients(client_id) ON DELETE SET NULL,
    purpose          text        NOT NULL
                                 CHECK (purpose IN ('login','bind','recover','stepup')),
    -- 发起该挑战的因子类型（开放）
    factor_type      text        NOT NULL,
    channel          text,                                   -- sms / email；holding 型为空
    target_masked    text,                                   -- 脱敏投递目标
    -- 期望响应的摘要（SM3/HMAC-SM3），恒定时间比较；明文码永不落库
    response_hash    bytea       NOT NULL,
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts     integer     NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    consumed_at      timestamptz,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT challenges_consumed_after_created
        CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CONSTRAINT challenges_expires_after_created
        CHECK (expires_at > created_at),
    CONSTRAINT challenges_attempts_bounded CHECK (attempts <= max_attempts)
);

-- 校验时按 id 命中（主键）；按用户/客户端排查近期挑战
CREATE INDEX challenges_user_created_idx ON challenges (user_id, created_at DESC);
CREATE INDEX challenges_client_created_idx ON challenges (client_id, created_at DESC);
-- 清理任务扫描过期未消费行
CREATE INDEX challenges_expires_idx ON challenges (expires_at)
    WHERE consumed_at IS NULL;

CREATE TRIGGER challenges_set_updated_at BEFORE UPDATE ON challenges
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();