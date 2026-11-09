package com.guanjin.spring.boot2;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.ReplayGuard;
import com.guanjin.core.replay.ReplayStore;
import org.junit.jupiter.api.Test;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

import java.time.Duration;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-DP-08 的 SDK 半边（"同 jti 二次 → 401"由验签方产生并记账）+ AC-SDK-06，**跑在真 Redis 上**。
 *
 * <p>这是本切片的核心证据：不是内存判重，而是 {@code SET NX PX} 跨请求真判重。
 */
class RedisReplayFilterIT {

    private static final String HEADER = "X-GuanJin-Assertion";

    @Test
    void AC_DP_08_sameJtiTwiceIs401AgainstRealRedis() throws Exception {
        LettuceConnectionFactory factory = RedisTestSupport.factory(RedisTestSupport.password());
        try {
            StringRedisTemplate template = new StringRedisTemplate(factory);
            template.afterPropertiesSet();
            GuanjinProperties props = props();
            GuanjinAssertionFilter filter = new GuanjinAssertionFilter(props,
                    new StubVerifier(props.getGateway().getIssuer(), props.resolvedAud()),
                    new ReplayGuard(new RedisReplayStore(template), Duration.ofSeconds(60)),
                    new PathMatcher(props.getPublicPaths(), props.isPublicPathsIncludeBuiltin()));

            String jti = "it-" + UUID.randomUUID();
            MockHttpServletResponse first = new MockHttpServletResponse();
            filter.doFilter(request("stub:" + jti), first, new MockFilterChain());
            assertEquals(200, first.getStatus(), "首次必须放行");
            assertEquals(Boolean.TRUE, template.hasKey(RedisReplayStore.KEY_PREFIX + jti),
                    "首次请求必须在真 Redis 里留下判重键");

            MockHttpServletResponse second = new MockHttpServletResponse();
            filter.doFilter(request("stub:" + jti), second, new MockFilterChain());
            assertEquals(401, second.getStatus(), "同 jti 二次必须 401");
            assertEquals(ErrorCodes.TOKEN_INVALID,
                    new ObjectMapper().readTree(second.getContentAsString()).get("code").asText());
            assertTrue(new ObjectMapper().readTree(second.getContentAsString())
                    .get("message").asText().contains("重放"));

            template.delete(RedisReplayStore.KEY_PREFIX + jti);
        } finally {
            factory.destroy();
        }
    }

    @Test
    void AC_SDK_06_redisOutageYields503RedisUnavailable() throws Exception {
        // 真 TCP：连接被重置 = Redis 挂掉（不依赖"服务端是否有口令"，CI 也能真红）
        RedisTestSupport.BlackHole hole = new RedisTestSupport.BlackHole();
        LettuceConnectionFactory factory = hole.factory();
        try {
            StringRedisTemplate template = new StringRedisTemplate(factory);
            template.afterPropertiesSet();
            ReplayStore broken = new RedisReplayStore(template);
            GuanjinProperties props = props();
            GuanjinAssertionFilter filter = new GuanjinAssertionFilter(props,
                    new StubVerifier(props.getGateway().getIssuer(), props.resolvedAud()),
                    new ReplayGuard(broken, Duration.ofSeconds(60)),
                    new PathMatcher(props.getPublicPaths(), props.isPublicPathsIncludeBuiltin()));

            MockHttpServletResponse res = new MockHttpServletResponse();
            filter.doFilter(request("stub:it-" + UUID.randomUUID()), res, new MockFilterChain());
            assertEquals(503, res.getStatus(), "存储不可用必须拒请求（不放行）");
            assertEquals(ErrorCodes.REDIS_UNAVAILABLE,
                    new ObjectMapper().readTree(res.getContentAsString()).get("code").asText());
        } finally {
            factory.destroy();
            hole.close();
        }
    }

    private static GuanjinProperties props() {
        GuanjinProperties p = new GuanjinProperties();
        p.getPlatform().setBaseUrl("http://127.0.0.1:18080");
        p.getClient().setId("orders");
        p.getClient().setHmacSecret("s3cr3t");
        return p;
    }

    private static MockHttpServletRequest request(String token) {
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, token);
        return req;
    }
}
