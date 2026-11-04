-- 000018 seed 注册挑战的两个 runtime_config 键（decisions.md D14①；判据 AC-REG-01）
--
-- 为什么必须是 runtime_config、不能是 DB 默认值或代码常量：
--   AC-REG-01 的判据原话是「attempts_left / TTL **等于 runtime_config 登记值**
--   （改一次键再跑，值必须跟着变 → 证明非硬编码）」。这两个值是**可运维调整的安全参数**，
--   不是结构性默认；任何 `if maxAttempts == 0 { 5 }` / `expires_at = now() + 5*time.Minute`
--   的写法都会让判据在"改键"后失效（那就是硬编码）。
--
-- 键名 / 取值 / 类型（D14① 冻结）：
--   challenge.ttl_seconds  = 300  int  一次性挑战有效期（秒）；skip 分支的 register_token 同用
--   challenge.max_attempts = 5    int  单挑战允许的失败校验次数（attempts_left 初值）
--
-- 幂等：ON CONFLICT (key) DO NOTHING —— 运维已改过的值不得被重跑覆盖
--       （与 000015 的 flow.ttl_seconds 同款；技术栈 §6.2：编号只增不改）。
-- 本迁移**不改** 000006 / 000014：已 up 过的库不会因改历史迁移而静默缺 seed。

INSERT INTO runtime_config (key, value, value_type, description)
VALUES
    ('challenge.ttl_seconds', '300'::jsonb, 'int',
     '一次性挑战有效期（秒）；注册窗口（register_token）同用。缺失 = 启动 fail-fast（D14①）'),
    ('challenge.max_attempts', '5'::jsonb, 'int',
     '单挑战允许的失败校验次数 = attempts_left 初值。缺失 = 启动 fail-fast（D14①）')
ON CONFLICT (key) DO NOTHING;

-- 单一来源：移除 `challenges.max_attempts` 的 DB 默认值（D14①）。
--   留着 DEFAULT 5 就等于给"忘记传值"留一条静默成功路径：键改成 3、代码漏传，库里仍是 5，
--   AC-REG-01 红在别人身上却查不出原因。移除后漏传立即 23502（NOT NULL）或 23514（CHECK > 0）。
--   本列取值**唯一来源** = runtime_config 的 `challenge.max_attempts`。
ALTER TABLE challenges ALTER COLUMN max_attempts DROP DEFAULT;
