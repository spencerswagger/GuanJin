// Package credential 是「凭据」层：口令派生信封、口令策略、TOTP 动态口令。
//
// 边界（decisions.md + 基线）：
//   - 本包**只做凭据语义**（信封结构、比对规则、策略判定），密码学原语一律走
//     `internal/crypto`（SM3/HMAC-SM3/SM4/PBKDF2-SM3 的唯一实现点）；
//   - 不碰数据库、不碰 Redis、不碰 HTTP：`credential_data jsonb` 的落库/读取是
//     `internal/store` 的事，一次性挑战的状态在 `challenges` 表（D5/D3-D）；
//   - **不持有主密钥**：需要解封 TOTP 共享密钥时，由调用方把 `crypto.SM4Key`
//     （已按 D4-B 派生）传进来，本包不认识 `MasterKey`。
//
// 硬约束：
//   - 口令 `PBKDF2-SM3`，迭代 ≥100000、盐 ≥16B（索引 §2；AC-AUD-12④）；信封里出现
//     低于下限的参数（被篡改、或历史遗留的弱参数）**读取即报错**，不"兼容性放行"；
//   - 三码固定顺序 `WEAK_PASSWORD → PASSWORD_REUSED → PASSWORD_POLICY_VIOLATION`，
//     对外只回首个、全量命中进 `details.violations[]`（AC-REG-10 / D3-B″）；
//   - TOTP 的 HMAC 键是**用户侧共享密钥**（不是平台密钥），走
//     `crypto.HMACSM3WithUserSecret`；同一时间窗内二次使用 → `TEMPORAL_CODE_REUSED`。
package credential
