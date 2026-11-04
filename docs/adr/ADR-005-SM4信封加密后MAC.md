# ADR-005 · SM4 信封改为 enc-then-MAC（先验 MAC 再解密）

| 项 | 内容 |
|---|---|
| 状态 | **Accepted**（Edge 起草；**Ark 批准 2026-11-04**。接受依据：**Owl 追认 #33 无阻断**（前向修复即可）+ **代码现状**——`internal/crypto/purpose.go` 已按本 ADR 落 8 个用途域（main `18ed08e`）；配套裁定 `decisions.md` **D15**。批准不改本 ADR 已定内容） |
| 日期 | 2026-11-04 |
| 主笔 | 数据面与国密 · Edge（H8CSJ5） |
| 上位依据 | `decisions.md` **D4-B**（HKDF-SM3 派生 / 一密钥一用途）、**D14②**（用途域 5→6 的先例；本 ADR 再 +2 → **8**）、**D8-E**（密评出证后置，但等保代码判据不动）、`AC-AUD-12`（应用侧国密）；基线 `数据模型 §3.3`（`identifiers.value_enc`）、`§3.7`（主密钥用法） |

---

## 1. 背景（问题是什么，为什么必须做决定）

`crypto.EncryptSM4` 的原格式是 `iv(16) || SM4-CBC(PKCS7(明文))`：**只有加密，没有完整性**。
`DecryptSM4` 能做的唯一校验是 PKCS#7 填充，而"随机字节恰好构成合法填充"的概率约 **1/256**。

后果不是"少一层防护"，而是**故障被误分类**：错钥（或密文被改）解密时，有约 0.4% 的样本
padding 恰好合法 → 返回一段乱码明文 → 调用方拿到的是"解密成功"，于是

- `internal/credential`（TOTP 信封）把"密钥不可用（`KEY_UNAVAILABLE`/503）"报成"凭据不对"；
- CI 表现为**随机刷红**：同一 commit 重跑就绿，命中的用例还散在不同包。

实证（2026-11-04）：

| 证据 | 结果 |
|---|---|
| CI 两次命中 | `internal/credential/TestTOTPOpenWithWrongKey`、`internal/factor/holding/TestTOTPVerifyResponseAndReplay` |
| 同一 commit `95d2b1c` | `push` run 红 / `pull_request` run 绿；`gh run rerun --failed` 后四 job 全绿 |
| 本机量化（对照探针，5000 次错钥解密，仅 PKCS#7 校验） | **18/5000 = 0.36%** 被误判为成功（理论 1/256 ≈ 0.391%） |

除了测试噪声，还有真实的完整性缺口：无 MAC 时**第 128 位翻转只污染对应的明文分组**，
其余分组照常解出，调用方无法区分"攻击者改过"和"正常数据"。

## 2. 决定（可执行口径 + 冻结名）

1. **密文格式 v2（唯一受支持）**：

   ```
   0x02 | iv(16) | SM4-CBC(PKCS7(明文)) | HMAC-SM3(32)
   ```

   `mac = HMAC-SM3(K_mac, 0x02 || iv || ct)`——**版本号也进 MAC**，防止抹掉/改掉版本字节降级。
   校验顺序固定：**先验 MAC（常量时间比较），再解密**；MAC 不通过一律返回 `ErrIntegrityMismatch`，
   不做任何解密、不返回半截明文。

2. **密钥：两条用途域成对**（D4-B 用途域累计 **5 → 8**，本 ADR 贡献 **6 → 8** 的两个 MAC 域；全量清单见 `decisions.md` D15③）：

   | 用途域 | 长度 | 用途 |
   |---|---|---|
   | `guanjin/sm4/field` | 16B | 敏感字段加密（既有） |
   | **`guanjin/hmac/field`** | 32B | 上述信封的完整性（新增） |
   | `guanjin/sm4/keywrap` | 16B | 密钥封装（既有） |
   | **`guanjin/hmac/keywrap`** | 32B | 上述信封的完整性（新增） |

   两把钥匙**都直接由主密钥按各自用途域派生**（HKDF-SM3，`salt = info = purpose`），
   **不是**"从 SM4 键再派生一把 MAC 键"——后者两把钥匙同源同命，用途隔离名存实亡。
   两个 MAC 域**不合并**：把"被包裹私钥"的密文挪进 `identifiers.value_enc` 应当验不过，
   靠的正是两把 MAC 键互不通用。

3. **类型承载**：`crypto.SM4Key` 同时持有加密材料与配套 MAC 材料，`EncryptSM4`/`DecryptSM4`
   **签名不变**，因此 `internal/credential`、`internal/identifier`、`internal/identity`、
   `internal/factor/holding`、`cmd/guanjind` 的调用点**零改动**。
   `SM4Key.IsZero()` 改为"加密材料或 MAC 材料任一为零即不可用"——只派生出半把钥匙的路径
   在入口就被挡住（`ErrKeyMissing`），写不出无完整性的密文。

4. **v1 一律拒绝**：`iv || 密文`（首字节不是 `0x02`）→ `ErrCiphertextVersion`。
   **不提供"没有 MAC 就按 CBC 解"的兼容分支**：留那个分支等于把完整性做成可选项，
   攻击者删掉 MAC 段就能降级——白做。

5. **存量密文**：项目**未上线**（P1 / spencer 2026-11-02「不考虑迁移」），因此
   **不做双读、不做自动重加密**；本地与预发库重灌（`migrate down/up` + 重跑）即可。
   **上线后**若需轮换密钥：跑一次性重加密任务（读 v2 → 用新键重新 `EncryptSM4` → 写回），
   **不引入 v1 读路径**；任何"灰度期同时接受 v1/v2"的提议须另行出 ADR。

## 3. 备选与不选的理由

- **(a) SM4-GCM（AEAD）**：`cipher.NewGCM(sm4.NewCipher(k))` 技术上可用，一步到位且不需要新的 MAC 用途域。
  **不选**：① `tjfoc/gmsm` 不提供 GCM 封装，需用 stdlib 自己拼；② 国标 SM4 工作模式
  （GB/T 36624）不含 GCM，密评材料要额外解释"为什么用国标外的组合"；③ 仍解决不了
  "加密与完整性要分属两把钥匙"的口径问题（GCM 把两者绑在一把钥匙上）。
- **(b) 保留现状 + 下游把"解密成功但内容不合理"再判一次**：治标不治本。概率不变，
  只是把误判从"密钥不可用"搬到"凭据格式错误"，而且篡改仍然无法检出。
- **(c) 复用 `hmac/identifier` / `hmac/audit` 当 MAC 键**：违反一密钥一用途（D4-B），
  且会把"换盲索引密钥"与"历史密文可读性"绑死。
- **(d) 给 v1 留兼容读路径**：见决定 4，等于给完整性校验开后门。

## 4. 后果

**正面**

- 错钥 / 篡改**必然**失败（`ErrIntegrityMismatch`），不确定性从"约 1/256"归零；CI 不再随机红。
- 调用点零改动：成对材料封在 `SM4Key` 内，`EncryptSM4`/`DecryptSM4` 签名不变。
- 篡改可检出：`版本 / IV / 密文 / MAC` 任一段被改都会被拒（有逐位置用例）。

**负面 / 代价**

- 密文长度 = 明文 + 1 + 16 + 32（含 PKCS#7 填充）。`value_enc` / `private_key_enc` 都是 `bytea`、
  无长度约束，**不需要改迁移**。
- 用途域累计 5 → 8（本 ADR 6 → 8）：**已在 `decisions.md` D15③ 登记**（2026-11-04，含冻结清单、顺序与生效范围），本文档不再另存一份清单。`hmac/field|keywrap` 一旦轮换，
  历史密文解不开（与其它用途同性质，需走重加密）。
- **旧密文不可读**：已写入本地/预发库的 `secret_enc`、`value_enc` 需要重灌（未上线，无业务影响）。

**需要谁在什么时候做什么**

| 谁 | 做什么 | 何时 |
|---|---|---|
| **Ark** | **已完成 2026-11-04**：批准本 ADR（状态 `Accepted`，依据 Owl 追认 #33 + 代码现状）；`decisions.md` **D15** 已落（用途域累计 **5 → 8** + v1 拒绝口径 + 对外码） | 已关闭 |
| **Gate** | `AC-AUD-12` 的应用侧国密断言补一条反向用例：**篡改任一字节必拒**（当前只断言算法名） | M1 收口前 |
| **Core** | 仅注释同步（无代码改动）：`internal/identifier/blindindex.go` 的「SM4 密文（随机 IV，含 IV 前缀）」改为「含版本/IV/MAC」，`internal/credential/totp.go` 的 `SecretEnc` 注释同理 | 随手 |
| **Edge（我）** | 已落实现与用例；后续如接密码机（P2），MAC 与加密仍分别走 K_mac / K_enc | 已完成 |

## 5. 验证方式（可执行）

```bash
# 单元：格式布局 / 错钥必然失败（500 轮）/ 逐位置篡改 / v1 拒绝 / 跨用途拒绝 / 半把钥匙拒绝
go test ./internal/crypto -run 'TestSM4' -count=1 -v

# 用途表自检：每个 SM4 用途必须有配套 32B HMAC 用途；MAC 材料必须等于配对的 DeriveHMAC
go test ./internal/crypto -run 'TestPurposeSpecsAreConsistent|TestSM4EnvelopeMacKeyIsThePairedPurpose' -count=1 -v

# 下游稳定（修复前 ~1/256 概率红）
go test ./internal/credential -run TestTOTPOpenWithWrongKey -count=200 -v
go test ./internal/factor/holding -run TestTOTPVerifyResponseAndReplay -count=200 -v
```

**变异验证**（两条都实跑过，删掉防护必须红）：

| 变异 | 期望 |
|---|---|
| 把 MAC 校验改成恒真（`if false && ...`） | `TestSM4WrongKeyAlwaysFails` 报「第 0 轮：错钥必须报 ErrIntegrityMismatch，实际 pkcs7 padding invalid」；`TestSM4TamperDetected` 报「第 1 字节被改竟然解开了」 |
| 给 v1 加降级解密分支 | `TestSM4LegacyV1Rejected` 报「v1 无认证密文必须报 ErrCiphertextVersion，实际 <nil>（明文 legacy-ciphertext）」 |

## 6. 变更历史

- **v2（2026-11-04）**：状态 `Proposed` → **`Accepted`**（接受依据 = Owl 追认 #33 无阻断 + 代码现状已按本 ADR 落地 `internal/crypto/purpose.go` 8 域）。**不改已定内容**；只把「待办」区与用途域计数改成现状：用途域统一表述为**累计 5 → 8**（本 ADR 6 → 8），全量清单的唯一出处 = `decisions.md` **D15③**。
- **v1（2026-11-04）**：首版。触发 = CI 两次随机红（同一 commit 重跑即绿，两次命中不同包的不同用例），
  根因 = `EncryptSM4` 只有 PKCS#7 校验、无 MAC；本机量化 18/5000 ≈ 0.36% 错钥被误判为成功。
