package com.guanjin.spring.boot2;

import com.guanjin.core.replay.ReplayStore;
import com.guanjin.core.replay.ReplayStoreUnavailableException;
import org.springframework.dao.DataAccessException;
import org.springframework.data.redis.core.StringRedisTemplate;

import java.time.Duration;
import java.util.concurrent.TimeUnit;

/**
 * {@code jti} 防重放的 Redis 实现（默认，契约 ✅C-4 / AC-SESS-05）。
 *
 * <p>两点契约要求：
 * <ul>
 *   <li><b>不自读 Redis 地址</b>（契约 §3.5）：复用宿主容器里已注入的 {@link StringRedisTemplate}，
 *       连接参数、密码、超时全由宿主负责，SDK 不新增配置键；</li>
 *   <li><b>存储不可用即拒请求</b>（AC-SDK-06）：任何 {@link DataAccessException} 都翻成
 *       {@link ReplayStoreUnavailableException}，上层映射 503 {@code REDIS_UNAVAILABLE}，
 *       **绝不返回 true 放行**。</li>
 * </ul>
 *
 * <p>语义靠 Redis 的 {@code SET key 1 NX PX ttl} 原子性：跨副本共享同一份判重账本。
 */
public class RedisReplayStore implements ReplayStore {

    /** 键前缀（本 SDK 私有命名空间，避免与宿主键冲突）。 */
    public static final String KEY_PREFIX = "guanjin:jti:";

    private final StringRedisTemplate redis;

    public RedisReplayStore(StringRedisTemplate redis) {
        if (redis == null) {
            throw new IllegalArgumentException("StringRedisTemplate 不能为 null（不许\"没客户端就放行\"）");
        }
        this.redis = redis;
    }

    @Override
    public boolean firstUse(String jti, Duration window) {
        long ttlMillis = window == null ? 60_000L : Math.max(1L, window.toMillis());
        Boolean first;
        try {
            first = redis.opsForValue().setIfAbsent(KEY_PREFIX + jti, "1", ttlMillis, TimeUnit.MILLISECONDS);
        } catch (DataAccessException e) {
            throw new ReplayStoreUnavailableException("Redis 不可用（SET NX PX 失败）：拒请求", e);
        } catch (RuntimeException e) {
            throw new ReplayStoreUnavailableException("Redis 客户端异常（SET NX PX 失败）：拒请求", e);
        }
        if (first == null) {
            // 管道/事务模式下拿不到结果 = 无法判定 → 按不可用处理（fail-closed）
            throw new ReplayStoreUnavailableException("Redis 未返回 SET NX 结果：无法判定，按不可用处理", null);
        }
        return first.booleanValue();
    }

    @Override
    public String name() {
        return "redis";
    }
}
