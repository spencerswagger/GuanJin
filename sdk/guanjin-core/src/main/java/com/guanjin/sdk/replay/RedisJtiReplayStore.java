package com.guanjin.sdk.replay;

import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinReplayException;
import java.time.Instant;

public final class RedisJtiReplayStore implements JtiReplayStore {

    public static final String DEFAULT_KEY_PREFIX = "guanjin:jti:";

    private final RedisCommands commands;
    private final String keyPrefix;

    public RedisJtiReplayStore(RedisCommands commands) {
        this(commands, DEFAULT_KEY_PREFIX);
    }

    public RedisJtiReplayStore(RedisCommands commands, String keyPrefix) {
        this.commands = commands;
        this.keyPrefix = keyPrefix == null ? DEFAULT_KEY_PREFIX : keyPrefix;
    }

    public static RedisJtiReplayStore forServer(String host, int port, String password, int timeoutMillis) {
        return new RedisJtiReplayStore(new SocketRedisCommands(host, port, password, timeoutMillis));
    }

    @Override
    public void checkAndStore(String jti, long expEpochSeconds) {
        if (jti == null || jti.isEmpty()) {
            throw new GuanjinReplayException("令牌缺少 jti，无法做重放检测");
        }
        long now = Instant.now().getEpochSecond();
        int ttl = (int) Math.max(expEpochSeconds - now, 1);
        boolean first;
        try {
            first = commands.setIfAbsent(keyPrefix + jti, "1", ttl);
        } catch (RuntimeException e) {
            throw new GuanjinDependencyException("jti 存储（Redis）不可用: " + e.getMessage(), e);
        }
        if (!first) {
            throw new GuanjinReplayException("jti 重放被拒绝: " + jti);
        }
    }
}
