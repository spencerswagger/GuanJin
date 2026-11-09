package com.guanjin.spring.boot2;

import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;

/**
 * 真 Redis 连接工厂（集成用例用）。**不 mock**：地址/密码来自环境变量，连不上就红。
 *
 * <p>环境变量（与 Go 侧 CI 口径一致）：{@code GUANJIN_TEST_REDIS_ADDR}（默认
 * {@code 127.0.0.1:6379}）、{@code GUANJIN_TEST_REDIS_PASSWORD}（默认空）。
 */
final class RedisTestSupport {

    private RedisTestSupport() {
    }

    static String host() {
        return address()[0];
    }

    static int port() {
        return Integer.parseInt(address()[1]);
    }

    static String password() {
        String p = System.getenv("GUANJIN_TEST_REDIS_PASSWORD");
        return p == null ? "" : p;
    }

    static LettuceConnectionFactory factory(String password) {
        LettuceConnectionFactory f = new LettuceConnectionFactory(host(), port());
        if (password != null && !password.isEmpty()) {
            f.setPassword(password);
        }
        f.afterPropertiesSet();
        return f;
    }

    static StringRedisTemplate template() {
        return new StringRedisTemplate(factory(password()));
    }

    private static String[] address() {
        String raw = System.getenv("GUANJIN_TEST_REDIS_ADDR");
        if (raw == null || raw.trim().isEmpty()) {
            raw = "127.0.0.1:6379";
        }
        String[] parts = raw.trim().split(":");
        return new String[]{parts[0], parts.length > 1 ? parts[1] : "6379"};
    }
}
