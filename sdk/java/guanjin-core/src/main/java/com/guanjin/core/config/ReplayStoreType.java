package com.guanjin.core.config;

/**
 * `guanjin.authz.replay-store` 的取值（契约 §3.2 #18）。
 *
 * <p>枚举值（`memory` / `redis`）是**概念层冻结值**，三语言逐字符相同：配置里只能写这两个小写串。
 *
 * <p>`redis` 是默认值（契约 §3.2 ✅C-4，依据 D6(5)①）：本项目多副本 + 无状态，默认 `memory`
 * 会在默认配置下静默降级为单机内存 → `jti` 防重放形同失效 = 索引 §2 禁止的降级放行。
 * `memory` **只允许在 `local|test` profile**（{@link GuanjinConfigValidator} 会挡）。
 */
public enum ReplayStoreType {

    /** 单机内存：仅 `local|test`（AC-SESS-05）。 */
    MEMORY("memory"),

    /** Redis：默认值（契约 ✅C-4），多副本唯一可用形态。 */
    REDIS("redis");

    private final String wireName;

    ReplayStoreType(String wireName) {
        this.wireName = wireName;
    }

    /** 配置/契约里的字面值（小写）。 */
    public String wireName() {
        return wireName;
    }

    /**
     * 严格解析：只接受 `memory` / `redis`（大小写不敏感、去空白），其余一律报错。
     *
     * <p>不做"猜"——写错一个字母必须启动失败，不能静默落到默认值（契约 §3.6 禁隐性逻辑）。
     */
    public static ReplayStoreType fromWire(String raw) {
        String v = raw == null ? "" : raw.trim();
        for (ReplayStoreType t : values()) {
            if (t.wireName.equalsIgnoreCase(v)) {
                return t;
            }
        }
        throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_REPLAY_STORE,
                "只允许 memory|redis，得到 \"" + raw + "\"");
    }
}
