package com.guanjin.sdk.replay;

import com.guanjin.sdk.GuanjinReplayException;
import java.time.Instant;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicBoolean;

public final class InMemoryJtiReplayStore implements JtiReplayStore {

    private final Map<String, Long> seen = new ConcurrentHashMap<>();

    @Override
    public void checkAndStore(String jti, long expEpochSeconds) {
        if (jti == null || jti.isEmpty()) {
            throw new GuanjinReplayException("令牌缺少 jti，无法做重放检测");
        }
        long now = Instant.now().getEpochSecond();
        long expiry = Math.max(expEpochSeconds, now + 1);
        AtomicBoolean replay = new AtomicBoolean(false);
        seen.compute(jti, (key, existing) -> {
            if (existing != null && existing > now) {
                replay.set(true);
                return existing;
            }
            return expiry;
        });
        if (replay.get()) {
            throw new GuanjinReplayException("jti 重放被拒绝: " + jti);
        }
        purgeExpired(now);
    }

    public int size() {
        return seen.size();
    }

    private void purgeExpired(long now) {
        seen.entrySet().removeIf(entry -> entry.getValue() <= now);
    }
}
