package com.guanjin.spring.boot2;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.assertion.Sm2AssertionVerifier;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.jwks.JwksClient;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.ReplayGuard;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

import javax.servlet.FilterChain;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import java.io.InputStream;
import java.net.URI;
import java.nio.charset.Charset;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * **端到端**：真网关签名路径产出的断言 → SDK 的 SM2 真验签 → 真 Redis 判重 → 剥离/注入。
 *
 * <p>向量由 `go run ./artifacts/local/gen-sdk-vectors`（import 平台 `internal/signing`）生成，
 * 即"数据面签发断言的那份代码"；**不是自造 claim、不是自搓签名**。
 *
 * <p>JWKS 走真 HTTP（{@link TestPlatform} 的 {@code /.well-known/jwks.json}），
 * 判重走真 Redis（`SET NX PX`），SM2 验签走 BouncyCastle。
 */
class Sm2FilterIT {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final String HEADER = "X-GuanJin-Assertion";

    private TestPlatform platform;
    private LettuceConnectionFactory redisFactory;
    private StringRedisTemplate redis;
    private JsonNode fixture;

    @BeforeEach
    void setUp() throws Exception {
        fixture = loadFixture();
        platform = new TestPlatform();
        platform.jwksBody(fixture.get("jwks").toString());
        redisFactory = RedisTestSupport.factory(RedisTestSupport.password());
        redis = new StringRedisTemplate(redisFactory);
        redis.afterPropertiesSet();
    }

    @AfterEach
    void tearDown() {
        platform.close();
        redisFactory.destroy();
    }

    @Test
    void AC_DP_03_realGatewaySignedAssertionPassesEndToEnd() throws Exception {
        GuanjinProperties props = props();
        GuanjinAssertionFilter filter = filter(props);
        String jti = fixture.get("vectors").get(0).get("expectedJti").asText();
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);

        final AtomicReference<GuanjinPrincipal> seen = new AtomicReference<GuanjinPrincipal>();
        final AtomicReference<String> headerSeen = new AtomicReference<String>("unset");
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                javax.servlet.http.HttpServletRequest r = (javax.servlet.http.HttpServletRequest) request;
                seen.set(GuanjinContextHolder.current());
                headerSeen.set(r.getHeader(HEADER));
            }
        };
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter.doFilter(request(vector("valid")), res, chain);

        assertEquals(200, res.getStatus(), "真网关签出的断言必须验签通过");
        assertEquals("alice", seen.get().username());
        assertEquals("user-1", seen.get().subject());
        assertEquals("AAL2", seen.get().acr());
        assertNull(headerSeen.get(), "交给业务 handler 的视图里不得再有该头");
        assertEquals(Boolean.TRUE, redis.hasKey(RedisReplayStore.KEY_PREFIX + jti),
                "判重账本必须落在真 Redis 上");
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);
    }

    @Test
    void AC_SDK_08_tamperedAndUnknownKidAndExpiredAreAll401() throws Exception {
        GuanjinProperties props = props();
        GuanjinAssertionFilter filter = filter(props);
        for (String name : new String[]{"tamperedPayload", "unknownKid", "expired", "futureIat",
                "wrongIss", "wrongAud", "oidcAlg"}) {
            MockHttpServletResponse res = new MockHttpServletResponse();
            filter.doFilter(request(vector(name)), res, new MockFilterChain());
            assertEquals(401, res.getStatus(), "向量 " + name + " 必须 401");
            assertEquals(ErrorCodes.TOKEN_INVALID,
                    new ObjectMapper().readTree(res.getContentAsString()).get("code").asText(), name);
        }
        // 反向：合法向量仍是 200（不是"一律 401"的假绿）
        String jti = fixture.get("vectors").get(0).get("expectedJti").asText();
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);
        MockHttpServletResponse ok = new MockHttpServletResponse();
        filter.doFilter(request(vector("valid")), ok, new MockFilterChain());
        assertEquals(200, ok.getStatus());
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);
    }

    @Test
    void AC_DP_08_replayOfRealGatewayJtiIs401() throws Exception {
        GuanjinProperties props = props();
        GuanjinAssertionFilter filter = filter(props);
        String jti = fixture.get("vectors").get(0).get("expectedJti").asText();
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);

        MockHttpServletResponse first = new MockHttpServletResponse();
        filter.doFilter(request(vector("valid")), first, new MockFilterChain());
        assertEquals(200, first.getStatus());

        MockHttpServletResponse second = new MockHttpServletResponse();
        filter.doFilter(request(vector("valid")), second, new MockFilterChain());
        assertEquals(401, second.getStatus(), "同 jti 二次（真 Redis）必须 401");
        assertTrue(new ObjectMapper().readTree(second.getContentAsString())
                .get("message").asText().contains("重放"));
        redis.delete(RedisReplayStore.KEY_PREFIX + jti);
    }

    // ---- helpers ----

    private GuanjinAssertionFilter filter(GuanjinProperties props) {
        JwksClient jwks = new JwksClient(URI.create(props.resolvedJwksUri()),
                new com.guanjin.core.http.UrlConnectionHttpFetcher(
                        props.getPlatform().getConnectTimeout(), props.getPlatform().getReadTimeout()),
                props.getGateway().getJwksCacheTtl(), props.getGateway().isJwksUnknownKidForceRefresh());
        return new GuanjinAssertionFilter(props, new Sm2AssertionVerifier(jwks),
                new ReplayGuard(new RedisReplayStore(redis), Duration.ofSeconds(60)),
                new PathMatcher(props.getPublicPaths(), props.isPublicPathsIncludeBuiltin()));
    }

    private GuanjinProperties props() {
        GuanjinProperties p = new GuanjinProperties();
        p.getPlatform().setBaseUrl(platform.baseUrl()); // 真 HTTP：JWKS 走 UrlConnectionHttpFetcher
        p.getClient().setId("orders");
        p.getClient().setHmacSecret("s3cr3t-test-only");
        return p;
    }

    private static MockHttpServletRequest request(String token) {
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, token);
        return req;
    }

    private String vector(String name) {
        for (JsonNode v : fixture.get("vectors")) {
            if (name.equals(v.get("name").asText())) {
                return v.get("token").asText();
            }
        }
        throw new AssertionError("向量不存在：" + name);
    }

    private static JsonNode loadFixture() throws Exception {
        InputStream in = Sm2FilterIT.class.getClassLoader().getResourceAsStream("vectors/assertion-vectors.json");
        if (in == null) {
            throw new IllegalStateException("缺跨语言向量：sdk/java/guanjin-core/src/test/resources/vectors/");
        }
        StringBuilder sb = new StringBuilder();
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) >= 0) {
            sb.append(new String(buf, 0, n, UTF8));
        }
        in.close();
        return new ObjectMapper().readTree(sb.toString());
    }
}
