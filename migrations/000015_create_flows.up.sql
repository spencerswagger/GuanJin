-- 000015 create flows（阶梯认证中间态的唯一权威落点）
-- 口径：decisions.md D5（13 列 + I-F1~I-F8）、D4-A（主键不写 DB 默认值）
-- 说明：challenges 不加 flow_id、不反查；流归属由请求显式携带 flow_id（CH-1）

CREATE TABLE flows (
    id              uuid        PRIMARY KEY,                 -- 即对外 flow_id，应用层 UUID v7
    user_id         uuid        REFERENCES users(id) ON DELETE SET NULL,      -- 匿名流为 NULL，完成时回填
    session_id      uuid        REFERENCES sessions(id) ON DELETE SET NULL,   -- 登录成功后一次性回填（I-F7）
    client_id       text        NOT NULL REFERENCES clients(client_id) ON DELETE RESTRICT,
    target_acr      text        NOT NULL DEFAULT 'AAL1'
                                CHECK (target_acr IN ('AAL1','AAL2','AAL3')),
    achieved_amr    text[]      NOT NULL DEFAULT '{}',        -- 单调不减（I-F2）
    completed_steps jsonb       NOT NULL DEFAULT '[]'::jsonb, -- [{factor,challenge_id,at}] 只追加
    status          text        NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','completed','expired','abandoned')),
    expires_at      timestamptz NOT NULL,                     -- TTL 取 runtime_config.flow.ttl_seconds（禁 DB 默认）
    consumed_at     timestamptz,                              -- 一次性消费标记
    trace_id        text,                                     -- 只写创建该流那次请求的 trace_id
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- I-F1 归属：completed 必须有 user_id；active 必须有 client_id
    CONSTRAINT flows_completed_requires_user
        CHECK ((status <> 'completed') OR (user_id IS NOT NULL)),
    -- I-F4 一次性：completed 与 consumed_at 同真同假
    CONSTRAINT flows_consumed_consistency
        CHECK ((status = 'completed') = (consumed_at IS NOT NULL)),
    CONSTRAINT flows_client_not_blank CHECK (length(btrim(client_id)) > 0)
);

-- 同一用户对同一业务系统至多一条活跃流（匿名流不受限）
-- 注意：带 WHERE 的部分唯一索引只能 CREATE UNIQUE INDEX，不得写成 CONSTRAINT
CREATE UNIQUE INDEX flows_one_active_per_user_client_uniq
    ON flows (user_id, client_id) WHERE status = 'active' AND user_id IS NOT NULL;

-- 会话撤销时批量作废
CREATE INDEX flows_session_idx ON flows (session_id) WHERE session_id IS NOT NULL;
-- TTL 清理器只扫活跃段
CREATE INDEX flows_expires_idx ON flows (expires_at) WHERE status = 'active';
-- 按业务系统排查/取证
CREATE INDEX flows_client_created_idx ON flows (client_id, created_at DESC);

CREATE TRIGGER flows_set_updated_at BEFORE UPDATE ON flows
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- flow.ttl_seconds 初值 600 显式登记（代码不得出现 600 常量；键缺失 = 启动 fail-fast）
INSERT INTO runtime_config (key, value, value_type, description)
VALUES ('flow.ttl_seconds', '600'::jsonb, 'int', '阶梯认证流 TTL（秒）；缺失即启动失败')
ON CONFLICT (key) DO NOTHING;
