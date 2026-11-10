# AC-ENG-04：pgxerr 八行映射的**逐行真库取证**（2026-11-11）

## 一句话

八行（CH-7①–⑧）**本来就在真库上各触发过一次**（`TestDBEveryUniqueViolationMapsOnce`），
但账目上仍是 PENDING —— 因为缺一份"逐行触发 + 实测 SQLSTATE + 映射结果"的可执行证据。
本次补的就是这份证据，并顺带把 AC-ENG-04 覆盖的**其他 SQLSTATE 类别**（外键 / NOT NULL /
CHECK / 无分区 / 只增表 / 22P02 / 22003 / 40001 / 40P01 / 42P01 / 42703 / 57P01）也各真触发一次。

**发现 1 处映射缺陷**：23514 且**无约束名**（真形态 = "落不进任何分区"）原来落 400
`INVALID_PARAMETER`，等于把**部署缺陷**说成"改参数重试"。已修为未映射（fail-closed → 500）。

## 逐行矩阵（本地真 PG 17.11，`TestAC_ENG_04_RealSQLStateMatrix`，日志前缀 `AC_ENG_04|`）

| 行 | 触发方式（真库） | 实测 SQLSTATE | 约束名 | code / 状态 | fail-closed |
|---|---|---|---|---|---|
| CH-7① | `identifiers` (type,value) 重复插入 | 23505 | `identifiers_type_value_uniq` | IDENTIFIER_ALREADY_BOUND / 409 | 否 |
| CH-7② | `users.username` 大小写不同但 `lower()` 相同 | 23505 | `users_username_lower_uidx` | IDENTIFIER_ALREADY_BOUND / 409 | 否 |
| CH-7③ | `role_bindings` (user,client,role) 重复 | 23505 | `role_bindings_uniq` | BINDING_ALREADY_EXISTS / 409 | 否 |
| CH-7④ | `policy_snapshots` (client,version) 重复 | 23505 | `policy_snapshots_uniq` | POLICY_SNAPSHOT_CONFLICT / 409 | 否 |
| CH-7⑤ | 同一 user×client 第二条 active 流 | 23505 | `flows_one_active_per_user_client_uniq` | COOLDOWN_ACTIVE / 409 | 否 |
| CH-7⑥ | `clients` 主键重复 | 23505 | `clients_pkey` | INVALID_CLIENT / 400 | 否 |
| CH-7⑦ | `signing_keys.kid` 重复 | 23505 | `signing_keys_kid_uniq` | INVALID_PARAMETER / 400 | 否 |
| CH-7⑧a | 每档位第二份 `tier_default` | 23505 | `login_policies_tier_default_uidx` | INVALID_PARAMETER / 400 | 否 |
| CH-7⑧b | 每 client 第二份 `client_override` | 23505 | `login_policies_client_override_uidx` | INVALID_PARAMETER / 400 | 否 |
| R-23503 | `flows.user_id` 指向不存在的用户 | 23503 | `flows_user_id_fkey` | USER_NOT_FOUND / 404 | 否 |
| R-23502 | `role_bindings.role` 传 NULL | 23502 | —（无约束名） | MISSING_PARAMETER / 400 | 否 |
| R-23514 | `sessions.acr` 超出 D12 取值域 | 23514 | `sessions_acr_check` | INVALID_PARAMETER / 400 | 否 |
| **R-23514-NOPART** | `audit_logs` 写入没有分区的月份（2099-01） | 23514 | —（**无约束名**） | **UNMAPPED → 500** | **是（本次修）** |
| R-23001 | UPDATE 只增表 `auth_events`（`forbid_mutation`） | 23001 | `forbid_mutation` | UNMAPPED → 500 | 是 |
| R-22P02 | 把 `'not-a-uuid'` 当 uuid 传参 | 22P02 | — | INVALID_PARAMETER / 400 | 否 |
| R-22003 | 把 `99999999999` 当 int4 | 22003 | — | INVALID_PARAMETER / 400 | 否 |
| R-42P01 | 查询不存在的表（迁移没跑） | 42P01 | — | UNMAPPED → 500 | 是 |
| R-42703 | 查询不存在的列（模型与迁移不一致） | 42703 | — | UNMAPPED → 500 | 是 |
| R-40001 | REPEATABLE READ 并发更新同一行 | 40001 | — | 无业务码，`Retryable=true` | 否（可重试） |
| R-40P01 | 两条事务反向 `FOR UPDATE` | 40P01 | — | 无业务码，`Retryable=true` | 否（可重试） |
| R-57P01 | 服务端 `pg_terminate_backend` 踢掉本连接 | 57P01 | — | DB_UNAVAILABLE / 503 | 否（依赖不可用） |

## 关于"真 PG 起停"（连接类不只靠 mock）

本地把 PG **真停再真起**（`pg_ctl -m fast stop` → 查询 → `pg_ctl start`），实测：

```
[1] 基线              query OK
[2] pg_ctl -m fast stop   → server stopped
[3] 停库后（池内旧连接）   err=*pgconn.ConnectError | code=DB_UNAVAILABLE | status=503 | IsUnavailable=true
[4] 停库后（新连接池）     err=*pgconn.ConnectError | code=DB_UNAVAILABLE | status=503 | IsUnavailable=true
[5] pg_ctl start          → server started
[6] 恢复后（同进程同池）    query OK
```

CI 里 **不能**停 service 容器（`postgres:16` 由 Actions 托管），所以 CI 侧的连接层证据保留两条
**真实但不停容器**的路径：`pg_terminate_backend`（服务端真发 57P01）+ 既有
`TestDBOutageThroughKilledProxyIsDBUnavailable`（真 TCP 链路被掐断，无任何 mock）。

## 反向守线（防"把判据放松成恒绿"）

- `assertRow` 要求错误**必须**是 `*pgconn.PgError`（连接类除外，那行单独断言 `IsUnavailable`）；
- 期望 `code == ""` 的行**必须** `Mapped() == false`（fail-closed），不许"未知错误随便 500 也算过"；
- `TestAC_ENG_04_MatrixCoversEveryUniqueRule`：`uniqueRules` 与矩阵覆盖的约束名**必须一致**，
  新增唯一约束而矩阵没补行 → 红。
- 40001/40P01 断言 `Retryable(MapError(err)) == true` **且** `IsUnavailable(err) == false`
  （并发冲突不许被伪装成依赖不可用）。

## 缺陷与观察

1. **缺陷（已修）**：23514 无约束名 → 原来 400。改为未映射（500）。依据：CHECK 违反一定带约束名，
   空约束名只出现在"落不进分区"这类 DDL/运维缺陷上。
2. **观察（不改）**：40001/40P01 未映射 → HTTP 层会回 500，靠调用方 `Retryable` 重试。
   这符合现有设计（"不吞成业务码"），但如果将来要在 HTTP 面直接回 409/503，需要 Spec 定码。
3. **顺带补齐**：`Error.Reason` 在 23502 / 22P02 / 22003 / 未登记 23503 / 未登记 23514 这些分支
   原来是空串（排障日志少一句话），已补。
