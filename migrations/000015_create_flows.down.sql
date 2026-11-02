-- rollback 000015 flows（seed 的 runtime_config 键一并移除）
DELETE FROM runtime_config WHERE key = 'flow.ttl_seconds';
DROP TABLE IF EXISTS flows CASCADE;
