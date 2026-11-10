## 这块解决什么

S11 的 `AC-AUD-12`：**应用侧全链路国密实证**。⑤（全仓不得用 SHA 系/RSA，唯一白名单 PKCE S256）已由 #81 的可执行扫描判；本 PR 补 ①②③④⑥ 的**运行期**证据。

单开一个"证据"用例而不是散在各自包里：验收要看"一条链路里六个位置全是国密"，分散的用例单看每条都绿，但没人能一眼说清"哪六条对应判据的六项"。

## 六项怎么证

| 项 | 断言 |
|---|---|
| ⑥ 主密钥只派生 | `NewMasterKey` → `DeriveHMAC(audit)` / `DeriveSM4(field)`；`HMACKey`/`SM4Key` 字段全不导出，业务算法只能走 `HMACSM3`/`EncryptSM4`；不同用途域派生不同键（同消息 HMAC 不同） |
| ① 令牌 SM3withSM2 | `SignJWT` → header `alg=SM3withSM2`；`VerifyJWT` 用 SM2 公钥验过；header 改成 `RS256` 必验签失败（算法降级被拒） |
| ② 审计 HMAC-SM3 | `Chain.Tag` 32B、确定性；换 key / 换 `prev_tag` / 改内容三者都改 tag；并**真落库**（`Append` + `Load`）比对 tag |
| ③ 字段 SM4 | `EncryptSM4` 输出 `0x02` 信封、密文不含明文、解密还原；篡改末位 → `ErrIntegrityMismatch` |
| ④ 口令 PBKDF2-SM3 | `algo=PBKDF2-SM3`、`iterations ≥100000`、盐 ≥16B；同参数 `DerivePassword` 重算 == 信封 `dk` |
| ⑤ 全仓算法扫描 | #81 的 `scripts/check-guomi-algorithms.sh`（`make gates` + CI 真调用，自带 15 条注入自检） |

## A/B（证明断言不是摆设）

```
# 把 ① 的 Algorithm 改成 RS256
--- FAIL: TestAC_AUD_12_FullChainGuomi
    guomi_evidence_db_test.go:110: ①令牌 alg 必须是 SM3withSM2，实际 "RS256"
# 恢复后
--- PASS（0.69s）
```

## 附：canonical 口径留档

`notes/audit-canonical-口径-2026-11-10.md` —— #80 的 canonical 口径（写侧借 `json.Marshal` + 读侧 `UseNumber` + 共用 `canonicalNumber`），
第一句写死"**边界是 int64/uint64 取值域，不是 2^53**"（这条被改错过两轮），并列了三处已知边界
（>1e308、回落 float64 的碰撞、性能）。`docs/decisions.md` 是 Ark 的共享契约面，请 **@Ark** 落 D 条目。

## 证据

单测全绿；`-tags=integration -race -p 8` 45s / 0 FAIL；审计串行套件 `-p 1 -count=3` 全绿；
覆盖率逐模块达标（§9.3 下限）；`gofmt -l` 空、`go vet` 干净；`make gates` 绿。
