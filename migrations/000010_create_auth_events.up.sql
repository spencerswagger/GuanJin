-- 000010 auth_events —— 认证事件流水（只增不改不删）
-- 只增表：无 updated_at 列，仅挂 forbid_mutation 触发器。

CREATE TABLE auth_events (
    id            uuid        PRIMARY KEY,
    user_id       uuid        REFERENCES users(id) ON DELETE SET NULL,       -- 未识别用户可空
    identifier_id uuid        REFERENCES identifiers(id) ON DELETE SET NULL,
    session_id    uuid        REFERENCES sessions(id) ON DELETE SET NULL,
    client_id     text        REFERENCES clients(client_id) ON DELETE SET NULL,
    -- 开放集合：login / logout / otp_send / otp_verify / bind / unbind /
    --          recover / reset / stepup / lock / unlock / revoke ...
    event_type    text        NOT NULL,
    factor_type   text,                                       -- 参与因子（开放）
    result        text        NOT NULL
                              CHECK (result IN ('success','failure','locked','denied','error')),
    reason        text,                                       -- 失败分类（不含敏感信息）
    risk_score    smallint    CHECK (risk_score BETWEEN 0 AND 100),
    acr           text        CHECK (acr IN ('AAL1','AAL2','AAL3')),
    amr           text[]      NOT NULL DEFAULT '{}',
    ip            inet,
    user_agent    text,
    device_id     text,
    trace_id      text,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- 按业务系统 + 时间导出（合规要求"审计可按业务系统切分与导出"）
CREATE INDEX auth_events_client_time_idx ON auth_events (client_id, occurred_at DESC);
CREATE INDEX auth_events_user_time_idx   ON auth_events (user_id, occurred_at DESC);
CREATE INDEX auth_events_type_result_idx ON auth_events (event_type, result, occurred_at DESC);
-- 账号级异常检测：近期失败激增
CREATE INDEX auth_events_failure_idx ON auth_events (user_id, occurred_at DESC)
    WHERE result IN ('failure','locked');

-- 只增不改不删（基线硬要求）
CREATE TRIGGER auth_events_append_only
    BEFORE UPDATE OR DELETE ON auth_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();