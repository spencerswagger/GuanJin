-- 000017 seed login_policies 的三档 tier_default（S6 e2e 前置；decisions.md D13）
--
-- 为什么放在迁移里而不是"管理面初始化流程"：
--   三档（L1/L2/L3）是基线冻结的**结构性默认**（索引 §2「档位只有客户端档位一层」、§4.2-16
--   「门槛项落 login_policies」），它们的取值由基线规定（§4.2-15：L1/L2 → AAL1，L3 → AAL2），
--   不含任何环境/租户相关信息 → 属"参考数据"，与 000015 的 flow.ttl_seconds seed 同类。
--   走初始化流程会引入"首个管理员尚未存在就要配置策略"的先有鸡先有蛋问题，故不采用。
--
-- 编号只增不改（技术栈 §6.2；golang-migrate 只记版本、不校验 checksum）：
--   本迁移**不改 000012**，避免已迁移库静默缺失 seed。
--
-- 幂等：ON CONFLICT 对准部分唯一索引 login_policies_tier_default_uidx (tier) WHERE scope='tier_default'，
--   运维若已改过某档默认值，重跑不会覆盖（DO NOTHING）。
-- 主键：id 无 DB 默认值（D4-A 应用层 UUID v7），故 seed 显式给固定 UUID ——
--   它们只是满足 PK 的稳定字面量，**不承载业务含义**，不得被当作"档位 ID"引用。

INSERT INTO login_policies (id, scope, tier, client_id, min_acr)
VALUES
    ('01910000-0000-7000-8000-0000000000a1', 'tier_default', 'L1', NULL, 'AAL1'),
    ('01910000-0000-7000-8000-0000000000a2', 'tier_default', 'L2', NULL, 'AAL1'),
    ('01910000-0000-7000-8000-0000000000a3', 'tier_default', 'L3', NULL, 'AAL2')
ON CONFLICT (tier) WHERE scope = 'tier_default' DO NOTHING;
