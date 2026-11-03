-- 000002 create clients
-- 口径：decisions.md D4（不写 DB 默认值 / 显式约束名）、D2（purpose 六值）
-- 说明：主键 UUID v7 由应用层生成（D4-A），迁移不写任何 DEFAULT uuidv7()。

CREATE TABLE clients (
    client_id                 text        PRIMARY KEY,        -- 即 OAuth client_id
    name                      text        NOT NULL,
    tier                      text        NOT NULL DEFAULT 'L2'
                                          CHECK (tier IN ('L1','L2','L3')),
    status                    text        NOT NULL DEFAULT 'active'
                                          CHECK (status IN ('active','disabled')),
    redirect_uris             text[]      NOT NULL DEFAULT '{}',
    post_logout_redirect_uris text[]      NOT NULL DEFAULT '{}',
    -- 对接密钥（业务系统 → 关津的上报/拉策略通道；每业务系统一份，不共用）
    secret_hash               bytea,                          -- HMAC-SM3 盲索引/验证用
    secret_enc                bytea,                          -- SM4 密文（还原展示/重发）
    prev_secret_hash          bytea,                          -- 轮换过渡期
    secret_rotated_at         timestamptz,
    -- 声明元数据（SDK 上报的策略快照另存 policy_snapshots）
    default_role              text,                           -- 合成默认角色，如 authenticated_user
    -- 门槛项 E：建设方配套要求（平台登记/跟踪，不实现技术）
    pentest_required          boolean     NOT NULL DEFAULT false,
    soc_required              boolean     NOT NULL DEFAULT false,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT clients_tier_valid CHECK (tier IN ('L1','L2','L3'))
    -- 注：基线原 clients_redirect_unique 用 CHECK 内子查询（PG 禁止），已按 decisions.md D4-D
    --     删除，改由 internal/admin 去重校验 + 单测覆盖（不新增列）
);

CREATE INDEX clients_tier_idx   ON clients (tier) WHERE status = 'active';
CREATE INDEX clients_status_idx ON clients (status);

CREATE TRIGGER clients_set_updated_at BEFORE UPDATE ON clients
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
