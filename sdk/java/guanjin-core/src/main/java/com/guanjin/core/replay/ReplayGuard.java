package com.guanjin.core.replay;

import com.guanjin.core.error.GuanjinSdkException;

import java.time.Duration;
import java.time.Instant;

/**
 * {@code jti} 防重放判定（契约 GJ-CAP-07，Java 侧冻结入口 {@code ReplayGuard.firstUse(jti, exp)}）。
 *
 * <p>归属明确：**"同 {@code jti} 二次 → 401" 由验签方（SDK）产生并记账**，平台数据面不判重
 * （依据 {@code SDK契约:66/776}、{@code m1-slices.md:73}；AC-DP-08 的 SDK 半边）。
 *
 * <p>三种结果，**没有放行型失败**：
 * <ul>
 *   <li>首次 → {@code true}；</li>
 *   <li>窗口内重复 → 抛 401 {@code TOKEN_INVALID}；</li>
 *   <li>存储不可用 → 抛 503 {@code REDIS_UNAVAILABLE}（拒请求，AC-SDK-06）。</li>
 * </ul>
 */
public final class ReplayGuard {

    private final ReplayStore store;
    private final Duration replayWindow;

    public ReplayGuard(ReplayStore store, Duration replayWindow) {
        if (store == null) {
            throw new IllegalArgumentException("ReplayStore 不能为 null（不许\"没存储就放行\"）");
        }
        this.store = store;
        this.replayWindow = replayWindow == null ? Duration.ofSeconds(60) : replayWindow;
    }

    /**
     * 占用 {@code jti}。
     *
     * @param jti 断言里的 {@code jti}
     * @param exp 断言的过期时刻；{@code null} 时只按 {@code replay-window} 记
     * @return {@code true} = 首次使用
     * @throws GuanjinSdkException 401（重放）或 503 {@code REDIS_UNAVAILABLE}（存储不可用）
     */
    public boolean firstUse(String jti, Instant exp) {
        if (jti == null || jti.trim().isEmpty()) {
            throw GuanjinSdkException.tokenInvalid("断言缺少 jti（无法做防重放）");
        }
        Duration ttl = replayWindow;
        if (exp != null) {
            Duration untilExp = Duration.between(Instant.now(), exp);
            if (untilExp.compareTo(ttl) > 0) {
                ttl = untilExp;
            }
        }
        boolean first;
        try {
            first = store.firstUse(jti.trim(), ttl);
        } catch (ReplayStoreUnavailableException e) {
            // fail-closed：存储挂了 = 拒请求，绝不放行
            throw GuanjinSdkException.redisUnavailable(
                    "jti 存储不可用（replay-store=" + store.name() + "）：拒请求，不放行", e);
        }
        if (!first) {
            throw GuanjinSdkException.tokenInvalid("jti 重放：该 jti 在窗口 " + ttl.getSeconds() + "s 内已出现过");
        }
        return true;
    }

    public ReplayStore store() {
        return store;
    }

    public Duration replayWindow() {
        return replayWindow;
    }
}
