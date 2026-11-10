# `factors.disabled` 并发写缺陷：根因与修法（2026-11-11）

## 1. 确认的根因（不是"读已提交快照"，也不是"没开事务"）

写路径 `PlatformFactorRepo.SetEnabled` **本来就开了事务**（`db.Transaction`），也在事务里做了
`SELECT value::text FROM runtime_config WHERE key = 'factors.disabled' FOR UPDATE`。问题在于：

> **行锁锁的是"行"。这一行还不存在时，`FOR UPDATE` 一行都锁不到。**

于是 20 个并发首次 PATCH 全部读到"空集" → 各自算出 `next = ["password"]`、`changed = true`
→ 各自 `INSERT … ON CONFLICT DO UPDATE`（后到的覆盖同值）+ **各自追加一条审计**
→ 同一次真实变更被记两条审计（两条 `before=true`），链不自洽；enable/disable 交叉时
后写覆盖先写 = 丢更新。

**A/B 实测**（把 advisory 锁摘掉，其余不动）：`TestDBPlatformFactorConcurrentSetEnabledIsSerialized`
连跑 3 次 **3/3 红**，报 `审计不自洽：第 2 条 before=true，但上一条 after=false`；
#92 的 HTTP 用例同样 3/3 红（与它自己记录的失败信息一字不差）。

## 2. 选的修法：② `pg_advisory_xact_lock`（不是 seed、不是先 INSERT 占位）

在事务里**先**取 `pg_advisory_xact_lock(platformFactorLockKey)`，再保留原有的
`SELECT … FOR UPDATE` 作为第二道防线（行已存在时它仍是第一道；不存在时是空操作）。

| 维度 | ② advisory 锁（选它） | ① 迁移 seed 空数组行 | ③ 先 `INSERT … ON CONFLICT DO NOTHING` 再 `FOR UPDATE` |
|---|---|---|---|
| 多实例部署 | ✅ 数据库级锁，所有后端/副本共享，串行 | ✅ 但依赖"每个环境都跑过 seed" | ✅ 行锁 + 唯一索引等待，同样跨实例 |
| 行不存在时 | ✅ 与"行在不在"无关 | ⚠️ 只覆盖 seed 过的键；行被删/新键又回到原缺陷 | ✅ 先建行再锁 |
| 副作用 | ✅ 零（锁只在事务期持有） | ⚠️ 多一条迁移；seed 值要与代码 upsert 同形 | ⚠️ 让"同值幂等"的请求也建行（写一个占位值再读） |
| 与 `runtime_config` 其它键一致 | ✅ 其它键运行期只读、不参与并发改写；审计链已用同一手法（`chainLockKey`） | — | — |
| 与"审计同事务"约束冲突 | ✅ 事务级锁，提交/回滚自动释放，与 `audit.Chain.Append` 同事务无冲突 | ✅ | ✅ |
| 是否依赖 Ark 的键形态裁定 | ✅ **不依赖**：单行聚合键或逐因子键都成立 | ❌ 取决于最终键名 | ✅ 不依赖 |

**锁序（写进注释，防死锁）**：`factors advisory 锁 → runtime_config 行锁 → 审计链的 chainLockKey`；
不存在反向路径，不成环。

## 3. 验收（#92 一字不改）

- `TestACFLOW05_04_ConcurrentPatches`（#92 的用例，**原文件未改一字**）：修复前 3/3 红、
  修复后 **3/3 绿**。
- 存储层另加确定性用例 `TestDBPlatformFactorConcurrentSetEnabledIsSerialized`
  （用 `afterRead` 注入点把窗口张开，不靠"多跑几次碰运气"）：带锁 3/3 绿（~0.3s）、摘锁 3/3 红。
- `go test -tags=integration -p 8 ./internal/store/ ./cmd/guanjind/ ./internal/admin/ ./internal/flow/`
  连跑 3 轮全绿；全量集成 **752 PASS / 0 SKIP / 0 FAIL**；单测全绿；`-race` 绿；`make gates` 绿；
  覆盖率闸原值不动（store 79% floor 75、总 81.2% floor 60）。

## 4. 需要 Ark 的 seed：**按本修法不需要**

② 不依赖行存在，所以**不必**为 `factors.disabled` 出 seed 迁移。若 Ark 仍要 seed（例如为了让
"平台级禁用集"从第一天就有确定载体），形态必须与代码 upsert 同形：

```
key='factors.disabled', value='[]'::jsonb, value_type='json',
description='平台级被禁用的因子类型（jsonb 字符串数组；与客户端级 disabled_factors 取并集）',
is_secret=false, updated_by=NULL
```

且迁移必须 `ON CONFLICT (key) DO NOTHING`（不能覆盖运行期已改过的值）。

**注意（与 Ark 的键形态裁定相关）**：本修法的锁与"键是单行聚合还是逐因子"**无关**——
无论最后裁成哪种，都是同一把 advisory 锁保护整条因子启停路径，所以这次修复不会因为裁定结果返工；
但**读写键名**会随裁定改（那是 Core/Ark 的事，不在本 PR 范围）。

## 5. 顺带发现（不在本 PR 修，报给主持人）

`TestAuditPartitionJobRunsOnceAtStartup`（`cmd/guanjind`）在 `-p 8` 全量跑时偶发红
（`audit_logs_p202612 应已被补上`）：`internal/audit` 的 `TestMain` 结束时 `DropPartitions`，
与同批并行的 `cmd/guanjind` 用例抢同一批分区。与本次改动无关（本 PR 只碰平台因子启停），
单独一次全量复跑即绿（752/0/0），建议后续把 audit 的收尾 DROP 限定在自己的 schema 或加互斥。
