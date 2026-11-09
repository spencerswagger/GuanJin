package com.guanjin.core.replay;

import com.guanjin.core.config.TestConfigs;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-DP-08 的 SDK 半边（"同 jti 二次 → 401"由验签方产生并记账）+ AC-SDK-06（存储不可用 → 拒请求）。
 */
class ReplayGuardTest {

    @Test
    void AC_DP_08_secondUseOfSameJtiIsRejectedAs401TokenInvalid() {
        ReplayGuard guard = new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60));
        String jti = "6f1d3f0e-9f1a-4b6e-9c2a-1f0b7f0c1234";
        assertTrue(guard.firstUse(jti, Instant.now().plusSeconds(60)), "首次必须放行");

        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> guard.firstUse(jti, Instant.now().plusSeconds(60)));
        assertEquals(401, e.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
        assertTrue(e.getMessage().contains("重放"), e.getMessage());
    }

    @Test
    void differentJtiIsIndependent() {
        ReplayGuard guard = new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60));
        assertTrue(guard.firstUse("jti-a", Instant.now().plusSeconds(60)));
        assertTrue(guard.firstUse("jti-b", Instant.now().plusSeconds(60)));
    }

    @Test
    void AC_SDK_06_unavailableStoreRejectsAs503RedisUnavailable() {
        ReplayStore broken = new ReplayStore() {
            @Override
            public boolean firstUse(String jti, Duration window) {
                throw new ReplayStoreUnavailableException("connection refused", null);
            }

            @Override
            public String name() {
                return "redis";
            }
        };
        ReplayGuard guard = new ReplayGuard(broken, Duration.ofSeconds(60));
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> guard.firstUse("jti-x", Instant.now().plusSeconds(60)));
        assertEquals(503, e.getStatus());
        assertEquals(ErrorCodes.REDIS_UNAVAILABLE, e.getCode());
        assertTrue(e.getMessage().contains("拒请求"), e.getMessage());
    }

    @Test
    void missingJtiIsRejected() {
        ReplayGuard guard = new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60));
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> guard.firstUse("  ", Instant.now().plusSeconds(60)));
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
    }

    @Test
    void windowExpiryAllowsReuseAfterTtl() throws Exception {
        InMemoryReplayStore store = new InMemoryReplayStore();
        ReplayGuard guard = new ReplayGuard(store, Duration.ofMillis(120));
        assertTrue(guard.firstUse("jti-t", Instant.now().plusMillis(120)));
        assertThrows(GuanjinSdkException.class,
                () -> guard.firstUse("jti-t", Instant.now().plusMillis(120)));
        Thread.sleep(200L);
        assertTrue(guard.firstUse("jti-t", Instant.now().plusMillis(120)),
                "窗口过期后同一个 jti 可以再用（按 exp/replay-window 记账）");
    }

    @Test
    void ttlIsAtLeastReplayWindowAndCoversExp() {
        final AtomicInteger seenTtl = new AtomicInteger();
        ReplayStore probe = new ReplayStore() {
            @Override
            public boolean firstUse(String jti, Duration window) {
                seenTtl.set((int) window.getSeconds());
                return true;
            }

            @Override
            public String name() {
                return "probe";
            }
        };
        ReplayGuard guard = new ReplayGuard(probe, Duration.ofSeconds(60));
        guard.firstUse("jti", Instant.now().plusSeconds(5));
        assertEquals(60, seenTtl.get(), "exp 比窗口短时，TTL 仍取 replay-window（60s）");

        guard.firstUse("jti2", Instant.now().plusSeconds(600));
        assertTrue(seenTtl.get() >= 599 && seenTtl.get() <= 600,
                "exp 比窗口长时，TTL 取到 exp（允许 1s 截断误差），实际=" + seenTtl.get());
    }

    @Test
    void memoryStoreIsLocalOnlyByConventionButNamed() {
        assertEquals("memory", new InMemoryReplayStore().name());
        // 多副本用 memory 会被 GuanjinConfigValidator 挡住（见 GuanjinConfigValidatorTest）
        TestConfigs.minimal();
    }
}
