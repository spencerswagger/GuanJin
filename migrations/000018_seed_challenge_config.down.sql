-- 000018 down：恢复列默认值、删除两个 seed 键（顺序与 up 相反）。
--
-- 恢复 DEFAULT 5 只是把库还原成 000006 定义的样子，**不是**把"5"重新变成合法取值来源：
-- 应用侧仍必须显式从 runtime_config 读（D14①）。

ALTER TABLE challenges ALTER COLUMN max_attempts SET DEFAULT 5;

DELETE FROM runtime_config
 WHERE key IN ('challenge.ttl_seconds', 'challenge.max_attempts');
