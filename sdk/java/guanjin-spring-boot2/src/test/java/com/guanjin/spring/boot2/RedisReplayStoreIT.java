package com.guanjin.spring.boot2;

import com.guanjin.core.replay.ReplayStore;
import com.guanjin.core.replay.ReplayStoreUnavailableException;
import org.junit.jupiter.api.Test;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;

import java.time.Duration;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-06 / AC-SESS-05 的**真 Redis** 证据（不许 mock）。
 *
 * <p>覆盖三件事：① {@code SET NX PX} 语义（首次 true、二次 false）；② 键真的带 TTL；
 * ③ Redis 不可用（AUTH 失败 / 端口不通）→ 抛 {@link ReplayStoreUnavailableException}（→ 503 拒请求）。
 */
class RedisReplayStoreIT {

    @Test
    void AC_SESS_05_firstUseIsAtomicAcrossCallsAndKeyCarriesTtl() {
        LettuceConnectionFactory factory = RedisTestSupport.factory(RedisTestSupport.password());
        try {
            StringRedisTemplate template = new StringRedisTemplate(factory);
            template.afterPropertiesSet();
            ReplayStore store = new RedisReplayStore(template);

            String jti = "it-" + UUID.randomUUID();
            assertEquals("redis", store.name());
            assertTrue(store.firstUse(jti, Duration.ofSeconds(60)), "首次使用必须放行");
            assertFalse(store.firstUse(jti, Duration.ofSeconds(60)), "同 jti 二次必须判重放");

            Long ttl = template.getExpire(RedisReplayStore.KEY_PREFIX + jti);
            assertNotNull(ttl);
            assertTrue(ttl > 0 && ttl <= 60, "键必须带 TTL（秒），实际=" + ttl);

            template.delete(RedisReplayStore.KEY_PREFIX + jti);
        } finally {
            factory.destroy();
        }
    }

    @Test
    void AC_SDK_06_wrongCredentialsMeansUnavailableNotPassThrough() {
        // 真 Redis：用错误口令 AUTH → 客户端报错（不是"没有响应"）
        LettuceConnectionFactory factory = RedisTestSupport.factory("definitely-wrong-password");
        try {
            StringRedisTemplate template = new StringRedisTemplate(factory);
            template.afterPropertiesSet();
            ReplayStore store = new RedisReplayStore(template);
            ReplayStoreUnavailableException e = assertThrows(ReplayStoreUnavailableException.class,
                    () -> store.firstUse("it-auth-" + UUID.randomUUID(), Duration.ofSeconds(60)));
            assertTrue(e.getMessage().contains("拒请求"), e.getMessage());
        } finally {
            factory.destroy();
        }
    }

    @Test
    void AC_SDK_06_closedPortMeansUnavailable() {
        LettuceConnectionFactory factory = new LettuceConnectionFactory("127.0.0.1", 6399);
        factory.afterPropertiesSet();
        try {
            StringRedisTemplate template = new StringRedisTemplate(factory);
            template.afterPropertiesSet();
            ReplayStore store = new RedisReplayStore(template);
            assertThrows(ReplayStoreUnavailableException.class,
                    () -> store.firstUse("it-closed-" + UUID.randomUUID(), Duration.ofSeconds(60)));
        } finally {
            factory.destroy();
        }
    }
}
