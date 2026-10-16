-- 000008 policy_snapshots —— SDK 上报的策略快照

CREATE TABLE policy_snapshots (
    id            uuid        PRIMARY KEY,
    client_id     text        NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
    version       integer     NOT NULL CHECK (version > 0),   -- 客户端内单调递增
    payload       jsonb       NOT NULL,                       -- 目录/映射整体
    payload_hash  bytea       NOT NULL,                       -- SM3(规范化 payload)
    integrity_tag bytea       NOT NULL,                       -- HMAC-SM3（关键数据存储完整性）
    reported_at   timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT policy_snapshots_uniq UNIQUE (client_id, version)
);

-- 取"最新一版"
CREATE INDEX policy_snapshots_client_version_idx
    ON policy_snapshots (client_id, version DESC);
-- payload 内检索（接口→权限映射查询）
CREATE INDEX policy_snapshots_payload_gin ON policy_snapshots USING gin (payload jsonb_path_ops);