-- rollback 000020：只删本迁移引入的键，不碰其它 runtime_config 键
-- 注意：删行后 concurrent PATCH 又回到"锁不到行"的窗口，且启动期校验会 fail-fast（D22）；
--       回滚请与回滚应用版本一起做。
DELETE FROM runtime_config WHERE key = 'factors.disabled';
