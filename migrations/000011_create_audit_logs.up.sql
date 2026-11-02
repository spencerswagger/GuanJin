-- 000011 create audit_logs（按月分区 + 链式完整性）
-- 口径：decisions.md D4-A（不写 DB 默认值）、D4-C（分区 + 主键含分区键 + 锚点在 runtime_config）

CREATE TABLE audit_logs (
    id            uuid        NOT NULL,
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
    ,
    -- D4-C：按月分区，主键必须包含分区键
    PRIMARY KEY (id, occurred_at)
    

) PARTITION BY RANGE (occurred_at);


CREATE INDEX audit_logs_actor_time_idx  ON audit_logs (actor_user_id, occurred_at DESC);
CREATE INDEX audit_logs_target_idx      ON audit_logs (target_type, target_id, occurred_at DESC);
CREATE INDEX audit_logs_client_time_idx ON audit_logs (client_id, occurred_at DESC);
CREATE INDEX audit_logs_action_time_idx ON audit_logs (action, occurred_at DESC);

-- 只增不改不删
CREATE TRIGGER audit_logs_append_only
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- 初始化分区：上月 / 当月 / 下月（此后由 cmd/guanjind 定时任务按月滚动创建）
DO $$
DECLARE
    m date;
BEGIN
    FOR i IN -1..1 LOOP
        m := date_trunc('month', now())::date + (i || ' month')::interval;
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF audit_logs FOR VALUES FROM (%L) TO (%L)',
            'audit_logs_p' || to_char(m, 'YYYYMM'), m, m + interval '1 month');
    END LOOP;
END $$;
