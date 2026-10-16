-- 000013 signing_keys —— 签名密钥

CREATE TABLE signing_keys (
    id              uuid        PRIMARY KEY,
    kid             text        NOT NULL UNIQUE,      -- JWKS 里对外可见的 key id
    purpose         text        NOT NULL CHECK (purpose IN ('oidc','gateway')),
    algorithm       text        NOT NULL CHECK (algorithm IN ('SM3withSM2','SM2')),
    status          text        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending','active','retiring','retired','compromised')),
    public_key      text        NOT NULL,             -- PEM / base64（可公开）
    private_key_enc bytea       NOT NULL,             -- SM4 密文（受主密钥保护，绝不落明文）
    activates_at    timestamptz,
    retires_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- 用途与算法绑定：对外 SM3withSM2，网关内部 SM2
    CONSTRAINT signing_keys_purpose_algo CHECK (
        (purpose = 'oidc'    AND algorithm = 'SM3withSM2') OR
        (purpose = 'gateway' AND algorithm = 'SM2')
    )
);

-- 发布 JWKS：只取某用途当前可用密钥
CREATE INDEX signing_keys_purpose_status_idx
    ON signing_keys (purpose, status);

CREATE TRIGGER signing_keys_set_updated_at BEFORE UPDATE ON signing_keys
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();