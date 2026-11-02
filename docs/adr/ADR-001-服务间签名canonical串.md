# ADR-001 · 服务间签名 canonical 串的唯一拼装口径（Agent 面 HMAC）

| 项 | 内容 |
|---|---|
| 状态 | Accepted |
| 日期 | 2026-11-02 |
| 主笔 | 关津 架构 · Ark（TWKJSV）；互验口径补口由 Nova（HBZ8HT）回读确认 |
| 上位依据 | `decisions.md` **D1**（含 A1 `PATH`、A2 `SORTED_QUERY`）；索引 §2「服务间认证」「令牌签名算法」；索引 §4.2-26；接口清单 §5.3.1 |

## 1. 背景

基线对「业务系统 → 平台」的 HMAC 签名存在**两套 canonical 串**：

- 索引 §4.2-26：`method \n path \n timestamp \n nonce \n sha256(body)`
- 接口清单 §5.3.1：`METHOD \n PATH \n sorted_query \n SHA256(body) \n timestamp \n nonce`

段集合不同（有没有 `sorted_query`）、段序不同，且两者都用 `SHA256`（与「全链路国密、禁 SHA 系列」冲突）。若由两端各自实现，SDK 与平台**必然互验失败**，且失败原因是"字符串不同"而非"签名错"，排查成本极高。

## 2. 决定

```
canonical = METHOD "\n" PATH "\n" SORTED_QUERY "\n" SM3HEX(body) "\n" TIMESTAMP "\n" NONCE
X-GuanJin-Signature = Base64( HMAC-SM3( secret, canonical ) )
```

冻结细则（逐条可执行，不得由实现自行发挥）：

| 段 | 口径 |
|---|---|
| `METHOD` | 大写（`GET`/`POST`…） |
| `PATH` | 客户端请求行里**实际发出的 origin-form path**：含全部前缀（含 context-path）、不含 scheme/host/port、不含 query/fragment、**不解码**、**不去尾斜杠**、空 path 按 `/` |
| `SORTED_QUERY` | 取 `?` 之后原串；空则空串；按 `&` 切、每段按**第一个** `=` 切；**保留重复键**；name/value 各自做 RFC3986 编码（**先编码再排序**）；排序键 = 编码后 name 字节序，name 相同再比编码后 value；用 `&` 连接 |
| `SM3HEX(body)` | body 的 SM3 摘要**小写 hex 64 字符**；空体取 `SM3("")` 的固定值，**不得省略该段** |
| `TIMESTAMP` | Unix 秒；窗口 **±300s**；越界 → `AGENT_TIMESTAMP_SKEW` |
| `NONCE` | **16~64 字节**的 `[A-Za-z0-9_-]`（base64url 无填充；按**字节**计长）；Redis 去重键 `agent:nonce:{client_id}:{nonce}`，TTL **600s**；重复 → `AGENT_NONCE_REPLAY` |
| 密钥 | 交付给业务方的 base64 文本**解码后的 32 字节原始密钥**；平台侧取 `clients.secret_enc` 的 SM4 明文；**不做二次编码** |
| 头名 | `X-GuanJin-Client` / `X-GuanJin-Timestamp` / `X-GuanJin-Nonce` / `X-GuanJin-Signature`（以接口 §5.3.1 为准） |

## 3. 备选与不选的理由

- **备选 A（用索引 §4.2-26 的四段式）**：否。它是"速查表"里的简写，没有 `sorted_query` → 带 query 的请求两端口径不可复现；段集合以**端点契约**为准（`D1` 依据②）。
- **备选 B（保留 `SHA256(body)`）**：否。索引 §2 明令签名算法全链路国密、禁与国家算法混用（密评一级高风险项）；故 `SHA256` → **`SM3`**。
- **备选 C（`PATH` 由框架 `Path` 字段取）**：否。框架/容器/中间件重写后的 path 与请求行原串可能不同，只有"请求行原样"是双方都能取到的同一事实。Go 用 `r.URL.EscapedPath()`、Java 用 `getRequestURI()`（含 contextPath，**不得** `getServletPath()`）、Node 用 `req.originalUrl.split('?')[0]`。

## 4. 后果

- **正面**：平台与三语言 SDK 有唯一互验口径；黄金向量可跨语言复用；错误可分型（时间戳/重放/签名三类码）。
- **负面 / 约束**：**部署红线**——Agent 面链路**禁止**在中间件做 path 重写/前缀剥离（nginx `rewrite`、ingress `rewrite-target` 等），出现即判 FAIL；若有统一前缀，必须让两边看到同一段 path。
- **需要谁做什么**：Nova 在 `guanjin-core` 按本 ADR 实现 HMAC 客户端；Edge 在 `internal/agent/hmac.go` 实现服务端；Gate 落黄金向量（空 query / 多 query 排序 / 空 body / 重复键）。

## 5. 验证方式

- `AC-AGENT-01/02`（黄金向量 A/B）+ `AC-AGENT-03`（拒绝路径：±300s、nonce 重放、长度字符集、secret 解码）。
- 三语言同一批 JSON 向量跑同一 runner，CI 比对输出逐字节一致。
- **注**：Agent 面判据归 **M2 前置**，不挂 M1 任何步骤；但向量文件允许在 S9 顺手落盘（不占 M1 门禁）。

## 6. 变更历史

- 2026-11-02 初版（`D1` + A1/A2 补口）。反悔成本：三语言实现 + 已发密钥的业务方全部要改 → 变更需 `D1` 修订 + ADR 替代版。
