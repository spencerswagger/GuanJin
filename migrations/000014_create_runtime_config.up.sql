-- 000014 runtime_config —— 运行时配置与平台参数（主密钥绝不入此表）

CREATE TABLE runtime_config (
    key         text        PRIMARY KEY,
    value       jsonb       NOT NULL,
    value_type  text        NOT NULL DEFAULT 'json'
                            CHECK (value_type IN ('string','int','bool','json','secret')),
    description text,
    is_secret   boolean     NOT NULL DEFAULT false,   -- true 时 value 内容为 SM4 密文（base64）
    updated_by  uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- 硬约束：SM4 主密钥不得存此表、不得以任何明文形式落库
    CONSTRAINT runtime_config_no_master_key CHECK (
        lower(key) !~ '(master[_-]?key|root[_-]?key|kek|sm4[_-]?master)'
    ),
    -- is_secret 与 value_type 一致
    CONSTRAINT runtime_config_secret_consistency CHECK (
        (is_secret = false) OR (value_type = 'secret')
    )
);

CREATE TRIGGER runtime_config_set_updated_at BEFORE UPDATE ON runtime_config
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();