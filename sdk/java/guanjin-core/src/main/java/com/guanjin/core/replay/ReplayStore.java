package com.guanjin.core.replay;

import java.time.Duration;

/**
 * {@code jti} 防重放存储（契约 GJ-CAP-07 的存储半边）。
 *
 * <p>两种实现：{@link InMemoryReplayStore}（仅 {@code local|test}）与
 * {@code guanjin-spring-boot2} 的 Redis 实现（默认，✅C-4）。
 *
 * <p>**fail-closed 契约**：存储不可用时必须抛 {@link ReplayStoreUnavailableException}
 * （→ 上层 503 {@code REDIS_UNAVAILABLE} 拒请求），**不许返回 true 放行**
 * （AC-SDK-06 / AC-SESS-05）。
 */
public interface ReplayStore {

    /**
     * 原子地"占用"一个 {@code jti}。
     *
     * @param jti    断言里的 {@code jti}（36 字符 UUID 形态）
     * @param window 占用时长（≥ {@code replay-window}）
     * @return {@code true} = 首次使用（放行）；{@code false} = 窗口内已出现过（重放 → 401）
     * @throws ReplayStoreUnavailableException 存储不可用（→ 503，拒请求）
     */
    boolean firstUse(String jti, Duration window);

    /** 实现名（{@code memory} / {@code redis}），用于启动自检打印与排障。 */
    String name();
}
