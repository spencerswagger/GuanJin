-- 000020 seed 平台级因子禁用集（decisions.md D22；判据 AC-FLOW-05 的物理载体）
--
-- 为什么必须有这一行（不是"顺手 seed 个默认值"）：
--   `SetEnabled` 的并发安全靠 `SELECT value::text FROM runtime_config WHERE key='factors.disabled' FOR UPDATE`
--   锁住这一行。**行不存在时 FOR UPDATE 锁不到任何东西** → 两个并发 PATCH 都读到"空集"→
--   后写覆盖先写（丢更新），且两条审计 before/after 都算错 = **一条真实变更记两条审计**（Core 的 #92 红证据）。
--   读侧"缺行按空集"只是不报错，堵不住这个窗口；把行 seed 出来才是锁的前提。
--
-- 值形态（D22 冻结）：**jsonb 空数组 `[]`**，`value_type='json'`、`is_secret=false`。
--   不用 `null`：`jsonb 'null'` 与"空集"不可区分，且 `jsonb_array_elements('null')` 直接报错，
--                 SQL 侧任何聚合都要额外兜一层。
--   不用 `{}`：对象不是集合；同样 `jsonb_array_elements` 报错，且与"按类型字符串列表"的语义不符。
--   非空时元素 = 因子类型字符串（如 `["sms"]`），集合语义：无序、无重复（写入侧去重并排序）。
--
-- 幂等：ON CONFLICT (key) DO NOTHING —— 运维已经改过禁用集时**不得**被重跑覆盖
--       （与 000015/000018/000019 同款；技术栈 §6.2：编号只增不改，故**不改** 000014）。
-- 回滚：down 按 key 精确删。注意：删掉后若重启，启动期校验会按"缺键"fail-fast（D22），
--       故回滚须与回滚应用版本一起做——这是 fail-closed 的**期望行为**，不是副作用。

INSERT INTO runtime_config (key, value, value_type, description, is_secret)
VALUES
    ('factors.disabled', '[]'::jsonb, 'json',
     '平台级被停用的因子类型（jsonb 字符串数组，空集 = []；与客户端级 login_policies.disabled_factors 取并集，D22）',
     false)
ON CONFLICT (key) DO NOTHING;
