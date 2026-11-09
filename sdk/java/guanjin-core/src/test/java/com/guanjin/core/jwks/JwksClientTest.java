package com.guanjin.core.jwks;

import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.http.HttpFetchException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;
import org.junit.jupiter.api.Test;

import java.net.URI;
import java.nio.charset.Charset;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * GJ-CAP-02/03/04 + AC-SDK-05 / AC-FC-04（JWKS 无缓存 → 503 KEY_UNAVAILABLE，不得降级放行）。
 *
 * <p>这里用**内存假 fetcher**（不是 mock 框架）：JWKS 是纯解析逻辑，真 Redis 才要求真连。
 */
class JwksClientTest {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final URI JWKS = URI.create("http://127.0.0.1:18080/.well-known/jwks.json");

    /** 平台真实形态：oidc（alg=SM2）+ gateway（alg=SM3withSM2）两把键。 */
    private static final String REAL_JWKS = "{\"keys\":["
            + "{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"oidc-1\",\"use\":\"sig\",\"alg\":\"SM2\","
            + "\"x\":\"11qq\",\"y\":\"22ww\"},"
            + "{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"gw-1\",\"use\":\"sig\",\"alg\":\"SM3withSM2\","
            + "\"x\":\"33ee\",\"y\":\"44rr\"}"
            + "]}";

    @Test
    void AC_SDK_03_refreshParsesRealJwksShapeAndSeparatesGatewayKeys() {
        JwksClient client = new JwksClient(JWKS, fixed(200, REAL_JWKS), Duration.ofMinutes(10), true);
        JwksSet set = client.refresh();
        assertEquals(2, set.size());
        assertEquals(2, set.kids().size());
        assertEquals(1, set.gatewayKeys().size(), "只有 alg=SM3withSM2 的键可验内部断言");
        assertEquals("gw-1", set.gatewayKeys().get(0).kid());
        assertNull(set.gatewayKey("oidc-1"), "用户令牌公钥不得用于验断言（GJ-CAP-02 密钥分离）");
        assertNotNull(client.publicKey("gw-1"));
        assertEquals("33ee", client.publicKey("gw-1").x());
    }

    @Test
    void AC_SDK_03_404IsFailureNotWarning() {
        JwksClient client = new JwksClient(JWKS, fixed(404, "{\"error\":\"not found\"}"),
                Duration.ofMinutes(10), true);
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class, client::refresh);
        assertEquals(503, e.getStatus());
        assertEquals(ErrorCodes.KEY_UNAVAILABLE, e.getCode());
        assertTrue(e.getMessage().contains("404"), e.getMessage());
        assertTrue(e.getMessage().contains("/.well-known/jwks.json"), e.getMessage());
    }

    @Test
    void AC_FC_04_unreachableJwksWithoutCacheIs503() {
        HttpFetcher boom = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException {
                throw new HttpFetchException("connect timed out", null);
            }
        };
        JwksClient client = new JwksClient(JWKS, boom, Duration.ofMinutes(10), true);
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class, () -> client.publicKey("gw-1"));
        assertEquals(503, e.getStatus());
        assertEquals(ErrorCodes.KEY_UNAVAILABLE, e.getCode());
    }

    @Test
    void AC_SDK_05_fetchFailureWithCacheKeepsUsingCacheForRealVerification() {
        final AtomicInteger calls = new AtomicInteger();
        HttpFetcher flaky = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException {
                if (calls.incrementAndGet() == 1) {
                    return new HttpResult(200, REAL_JWKS.getBytes(UTF8), "application/json");
                }
                throw new HttpFetchException("connection reset", null);
            }
        };
        JwksClient client = new JwksClient(JWKS, flaky, Duration.ZERO, true);
        assertNotNull(client.publicKey("gw-1"), "首次拉取成功");
        // TTL=0 → 缓存立即"过期" → 会尝试重拉，失败但有缓存 → 沿用（非降级）
        JwksKey key = client.publicKey("gw-1");
        assertEquals("gw-1", key.kid());
        assertTrue(calls.get() >= 2, "确实尝试过重拉：" + calls.get());
    }

    @Test
    void AC_SDK_04_unknownKidForcesOneRefreshThen401() {
        final List<String> served = new ArrayList<String>();
        HttpFetcher rotating = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                served.add("hit");
                String body = served.size() == 1
                        ? REAL_JWKS
                        : "{\"keys\":[{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"gw-2\",\"use\":\"sig\","
                        + "\"alg\":\"SM3withSM2\",\"x\":\"55tt\",\"y\":\"66yy\"}]}";
                return new HttpResult(200, body.getBytes(UTF8), "application/json");
            }
        };
        JwksClient client = new JwksClient(JWKS, rotating, Duration.ofMinutes(10), true);
        assertNotNull(client.publicKey("gw-1"));
        // 轮换后新 kid：强刷一次即拿到
        assertEquals("gw-2", client.publicKey("gw-2").kid());
        assertEquals(2, served.size());

        GuanjinSdkException e = assertThrows(GuanjinSdkException.class, () -> client.publicKey("gw-unknown"));
        assertEquals(401, e.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
    }

    @Test
    void unknownKidWithoutForceRefreshIsRejectedWithoutFetching() {
        final AtomicInteger calls = new AtomicInteger();
        HttpFetcher counting = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                calls.incrementAndGet();
                return new HttpResult(200, REAL_JWKS.getBytes(UTF8), "application/json");
            }
        };
        JwksClient client = new JwksClient(JWKS, counting, Duration.ofMinutes(10), false);
        assertNotNull(client.publicKey("gw-1"));
        assertEquals(1, calls.get());
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class, () -> client.publicKey("nope"));
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
        assertEquals(1, calls.get(), "关闭强刷时不得再回源");
    }

    @Test
    void malformedJwksIsKeyUnavailable() {
        JwksClient client = new JwksClient(JWKS, fixed(200, "not json"), Duration.ofMinutes(10), true);
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class, client::refresh);
        assertEquals(ErrorCodes.KEY_UNAVAILABLE, e.getCode());

        JwksClient empty = new JwksClient(JWKS, fixed(200, "{\"keys\":[]}"), Duration.ofMinutes(10), true);
        assertEquals(ErrorCodes.KEY_UNAVAILABLE,
                assertThrows(GuanjinSdkException.class, empty::refresh).getCode());
    }

    private static HttpFetcher fixed(final int status, final String body) {
        return new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                return new HttpResult(status, body.getBytes(UTF8), "application/json");
            }
        };
    }
}
