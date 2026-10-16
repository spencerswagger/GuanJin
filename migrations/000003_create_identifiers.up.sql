-- 000003 identifiers —— 指向用户的"名字"

CREATE TABLE identifiers (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- 开放集合：phone / email / username / employee_no / 未来新增（不加 CHECK）
    type          text        NOT NULL,
    -- 匹配用盲索引：HMAC-SM3(主密钥, 规范化值)。承担 (type,value) 全局唯一与等值查询
    value         bytea       NOT NULL,
    -- 还原用：SM4 密文（随机 IV，含 IV 前缀）
    value_enc     bytea       NOT NULL,
    -- 脱敏展示：138****8888 / a***@x.com
    value_masked  text        NOT NULL,
    verified      boolean     NOT NULL DEFAULT false,
    trust_level   smallint    NOT NULL DEFAULT 0 CHECK (trust_level BETWEEN 0 AND 3),
    is_primary    boolean     NOT NULL DEFAULT false,
    verified_at   timestamptz,
    verify_method text,                                  -- sms_otp / email_otp / admin ...
    last_used_at  timestamptz,
    status        text        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','disabled')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT identifiers_type_value_uniq UNIQUE (type, value),   -- 全局唯一
    CONSTRAINT identifiers_verified_consistency
        CHECK (NOT verified OR verified_at IS NOT NULL)
);

-- 每个用户至多一个"主标识符"
CREATE UNIQUE INDEX identifiers_one_primary_per_user
    ON identifiers (user_id) WHERE is_primary;

-- 按用户列出全部标识符（个人中心 / 找回渠道列表）
CREATE INDEX identifiers_user_idx ON identifiers (user_id);

CREATE TRIGGER identifiers_set_updated_at BEFORE UPDATE ON identifiers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();