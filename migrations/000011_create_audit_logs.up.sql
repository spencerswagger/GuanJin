-- 000011 audit_logs —— 管理操作审计（只增不改不删）
-- 只增表：无 updated_at 列，仅挂 forbid_mutation 触发器。

CREATE TABLE audit_logs (
    id            uuid        PRIMARY KEY,
    actor_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    actor_role    text,                                       -- sys_admin|sec_admin|auditor
    action        text        NOT NULL,                       -- user.disable / role.bind /
                                                              -- policy.update / key.rotate ...
    target_type   text,                                       -- user / role_binding / client / signing_key ...
    target_id     text,
    client_id     text,                                       -- 与某业务系统相关时填写
    before        jsonb,                                      -- 变更前（脱敏后）
    after         jsonb,                                      -- 变更后（脱敏后）
    result        text        NOT NULL CHECK (result IN ('success','failure')),
    -- 链式完整性：HMAC-SM3(主密钥, prev_tag || 规范化记录内容)
    integrity_tag bytea       NOT NULL,
    prev_tag      bytea,                                      -- 前一条的 integrity_tag（首条为 NULL / 锚定值）
    signature     bytea,                                      -- 关键操作 SM2 签名（抗抵赖），可空
    ip            inet,
    trace_id      text,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_actor_time_idx  ON audit_logs (actor_user_id, occurred_at DESC);
CREATE INDEX audit_logs_target_idx      ON audit_logs (target_type, target_id, occurred_at DESC);
CREATE INDEX audit_logs_client_time_idx ON audit_logs (client_id, occurred_at DESC);
CREATE INDEX audit_logs_action_time_idx ON audit_logs (action, occurred_at DESC);

-- 只增不改不删
CREATE TRIGGER audit_logs_append_only
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();