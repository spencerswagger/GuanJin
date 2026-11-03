-- 000001 create users
-- 口径：decisions.md D4（不写 DB 默认值 / 显式约束名）、D2（purpose 六值）
-- 说明：主键 UUID v7 由应用层生成（D4-A），迁移不写任何 DEFAULT uuidv7()。

CREATE TABLE users (
    id              uuid        PRIMARY KEY,
    username        text        NOT NULL,                    -- 规范化登录名（小写存储）
    display_name    text,                                    -- 展示名，可空
    status          text        NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','disabled')),
    is_admin        boolean     NOT NULL DEFAULT false,      -- 平台管理员标记（C 端用户为 false）
    disabled_at     timestamptz,
    disabled_reason text,
    -- 可选：生效配置的物化缓存（= 已访问业务系统中最高的 tier），由应用层维护
    effective_tier  text        CHECK (effective_tier IN ('L1','L2','L3')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_username_len CHECK (char_length(username) BETWEEN 3 AND 64),
    CONSTRAINT users_disabled_consistency
        CHECK ((status = 'disabled') = (disabled_at IS NOT NULL))
);

-- 用户名大小写不敏感唯一（登录不区分大小写）
CREATE UNIQUE INDEX users_username_lower_uidx ON users (lower(username));

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
