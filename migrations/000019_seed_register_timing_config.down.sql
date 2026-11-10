-- rollback 000019：只删本迁移引入的判据参数键，不碰其它 runtime_config 键
DELETE FROM runtime_config WHERE key = 'register.timing.max_diff_ms';
