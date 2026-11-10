# 网关时钟偏移环境变量改名：`GUANJIN_GATEWAY_CLOCK_SKEW` → `GUANJIN_GW_CLOCK_SKEW`

## 为什么改名

Spec 裁出真撞车：旧名**正是** SDK 契约键 `guanjin.gateway.clock-skew`（#10）的机械映射名
（`guanjin.` → `GUANJIN_`、`.`/`-` → `_`、全大写）。同一个名字两个所有者、语义不同：

- 网关侧：对上游断言/令牌的**宽容偏移**（`gateway.Options.ClockSkew`）；
- SDK 侧：校验 `exp/iat` 的窗口。

SDK 那 29 个映射名是**契约**（别名即违约），所以改网关侧。新名 `GUANJIN_GW_CLOCK_SKEW`
不在基线 §7.2 保留表内、全仓只此一处使用 → **零基线改动、零 CH**。

## 改了什么（1 文件）

`cmd/guanjingw/main.go`：
- 读取处 `durationEnv("GUANJIN_GATEWAY_CLOCK_SKEW", 0)` → `durationEnv("GUANJIN_GW_CLOCK_SKEW", 0)`；
- **新增启动日志**（对应"别让运维改完名以为没生效"）：
  - 未设置 → `WARN clock skew: 未设置 GUANJIN_GW_CLOCK_SKEW，使用默认 0ms（不容忍时钟偏移） effective_ms=0`
  - 已设置 → `INFO clock skew: 取自 GUANJIN_GW_CLOCK_SKEW effective_ms=<n>`
- 口径不变：**默认 0 继续跑，不 fail-fast**（"不容忍偏移"本来就是安全默认）；
  非法值（`sixty` / `0` / 负数）仍由 `durationEnv` 拒掉 → `startup failed` + 退出码 2。

## 残留检查（`grep -rn "GUANJIN_GATEWAY_CLOCK_SKEW"`，排除 `.git/`）

| 命中处 | 性质 |
|---|---|
| **代码/配置/脚本/测试夹具** | **0 处**（`grep -rn` 全仓只有下面两类） |
| `cmd/guanjingw/main.go:73` | 本 PR 的**改名说明注释**（刻意保留旧名，便于双向追溯） |
| `docs/decisions.md:634`、`:641` | **Ark 的登记表**（`docs/` 不是我的写者面）→ 请他改一行：把旧名换成 `GUANJIN_GW_CLOCK_SKEW` |

顺带核过一遍同类撞车：SDK 的 `guanjin.gateway.*` 共 8 个键，网关读的
`GUANJIN_GATEWAY_ROUTES` / `_UPSTREAM_TIMEOUT` / `_DIAL_TIMEOUT` **都不在**那张表里（`ROUTES`/`UPSTREAM_TIMEOUT`/
`DIAL_TIMEOUT` 不是 SDK 键）→ **`clock-skew` 是唯一真撞车**，其余不用动。

## 验证

- `go test ./cmd/guanjingw/ ./internal/gateway/...`：全绿。
- 全量集成一次：**760 PASS / 0 SKIP / 0 FAIL**。
- 覆盖率闸原值未动：`internal/gateway` 89%（floor 80）、总 81.3%（floor 60）；`make gates` 绿。
