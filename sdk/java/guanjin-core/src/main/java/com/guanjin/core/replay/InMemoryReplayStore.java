package com.guanjin.core.replay;

import java.time.Duration;
import java.util.Iterator;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;

/**
 * 单机内存实现：**只允许在 {@code local|test}**（契约 C-4 / AC-SESS-05）。
 *
 * <p>多副本部署下用它 = 每个副本各判各的 = {@code jti} 防重放失效 = 降级放行，
 * 因此 {@link com.guanjin.core.config.GuanjinConfigValidator} 会在非 {@code local|test}
 * 环境直接让启动失败。
 *
 * <p>没有后台线程：过期项在每次写入时按 1/64 概率顺手清一次（单测可复现，不依赖 GC）。
 */
public final class InMemoryReplayStore implements ReplayStore {

    private final ConcurrentHashMap<String, Long> seen = new ConcurrentHashMap<String, Long>();
    private final AtomicLong ops = new AtomicLong();

    @Override
    public boolean firstUse(String jti, Duration window) {
        if (jti == null || jti.isEmpty()) {
            throw new IllegalArgumentException("jti 不能为空");
        }
        long ttlMillis = window == null ? 60_000L : window.toMillis();
        long now = System.currentTimeMillis();
        if ((ops.incrementAndGet() & 63L) == 0L) {
            purgeExpired(now);
        }
        Long previous = seen.putIfAbsent(jti, now + ttlMillis);
        if (previous == null) {
            return true;
        }
        if (previous <= now) {
            // 上一轮的占用已过期：用 CAS 换成新一轮（仍是原子的）
            return seen.replace(jti, previous, now + ttlMillis);
        }
        return false;
    }

    @Override
    public String name() {
        return "memory";
    }

    /** 当前占用的 jti 数（测试与排障用）。 */
    public int size() {
        return seen.size();
    }

    private void purgeExpired(long now) {
        Iterator<Map.Entry<String, Long>> it = seen.entrySet().iterator();
        while (it.hasNext()) {
            Map.Entry<String, Long> e = it.next();
            if (e.getValue() != null && e.getValue() <= now) {
                it.remove();
            }
        }
    }
}
