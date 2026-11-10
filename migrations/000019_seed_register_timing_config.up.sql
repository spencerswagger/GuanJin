-- 000019 seed 注册面「账号存在性」耗时差阈值（decisions.md D21；判据 AC-REG-04 后半句）
--
-- 判据原文：AC-REG-04「已存在 email 再注册 → 403/409 IDENTIFIER_ALREADY_BOUND，
--   **耗时与不存在账号差异 ≤ 阈值（阈值取自 runtime_config）**」。
-- 基线侧只有定性要求、**没有数字**：技术栈 §anti-enumeration「注册的『账号是否存在』绝不通过
--   message/状态码/耗时泄露（接口清单 §3.6.1）」、页面规格 §「响应耗时保持一致」。
--   → 阈值只能由本台账裁定（D21），不得由实现或测试自造一个魔法数。
--
-- 键名 / 取值 / 类型（D21 冻结）：
--   register.timing.max_diff_ms = 50   int   两条路径耗时中位数之差的**上界**（毫秒，取绝对值）
--
-- 为什么是「判据参数」而不是「服务运行时参数」：
--   它约束的是**判据怎么判**（改这个键，判据的宽严必须跟着变 → 证明非硬编码），
--   服务实现**不读它、也不得据此改变任何对外行为**（尤其禁止 `sleep` 凑时长）。
--   因此**不进 StartupKeys()**：缺键 = 判据不可执行（e2e 直接红），不是生产启动失败。
--
-- 幂等：ON CONFLICT (key) DO NOTHING —— 运维已调整过的值不得被重跑覆盖
--       （与 000015/000018 同款；技术栈 §6.2：编号只增不改，故**不改** 000014/000018）。
-- 回滚：down 只删本键（按 key 精确删），不碰其它键。

INSERT INTO runtime_config (key, value, value_type, description)
VALUES
    ('register.timing.max_diff_ms', '50'::jsonb, 'int',
     'AC-REG-04 判据参数：register/start「标识符已存在」与「不存在」两条路径的耗时中位数之差上界（毫秒）。D21；服务不读，仅判据读')
ON CONFLICT (key) DO NOTHING;
