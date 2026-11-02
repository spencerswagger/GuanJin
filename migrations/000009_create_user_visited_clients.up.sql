-- 000009 create user_visited_clients
-- 口径：decisions.md D4（不写 DB 默认值 / 显式约束名）、D2（purpose 六值）
-- 说明：主键 UUID v7 由应用层生成（D4-A），迁移不写任何 DEFAULT uuidv7()。

CREATE TABLE user_visited_clients (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id     text        NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
    tier_at_visit text        CHECK (tier_at_visit IN ('L1','L2','L3')),  -- 记录时的档位（供追溯）
    visit_count   bigint      NOT NULL DEFAULT 1 CHECK (visit_count > 0),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT user_visited_clients_uniq UNIQUE (user_id, client_id)
);

-- 计算生效配置：按用户取最高档位（连客户端 tier）
CREATE INDEX user_visited_clients_user_idx ON user_visited_clients (user_id);
-- 运维统计"某业务系统有多少用户访问过"
CREATE INDEX user_visited_clients_client_idx ON user_visited_clients (client_id);

CREATE TRIGGER user_visited_clients_set_updated_at BEFORE UPDATE ON user_visited_clients
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
