package com.guanjin.core.startup;

import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.TestConfigs;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;
import com.guanjin.core.jwks.JwksClient;
import org.junit.jupiter.api.Test;

import java.net.URI;
import java.nio.charset.Charset;
import java.time.Duration;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** GJ-CAP-22 启动自检：AC-SDK-03（首启必须命中真实 JWKS，404 即失败）。 */
class GuanjinStartupCheckerTest {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final String JWKS_BODY = "{\"keys\":[{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"gw-1\","
            + "\"use\":\"sig\",\"alg\":\"SM2\",\"x\":\"aa\",\"y\":\"bb\"}]}";

    @Test
    void AC_SDK_03_startupFetchesJwksAndReportsGatewayKids() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.setPublicPaths(TestConfigs.list("/assets/*"));
        JwksClient jwks = new JwksClient(URI.create(cfg.resolvedJwksUri()), ok(JWKS_BODY),
                Duration.ofMinutes(10), true);
        StartupReport report = GuanjinStartupChecker.check(cfg, TestConfigs.prod(), jwks);
        assertEquals(TestConfigs.BASE_URL, report.platformBaseUrl());
        assertTrue(report.jwksUri().endsWith("/.well-known/jwks.json"));
        assertEquals(1, report.gatewayKids().size());
        assertEquals("gw-1", report.gatewayKids().get(0));
        // 放行清单 = 内置 + 显式声明（可审计）
        assertTrue(report.publicPaths().contains("/healthz"));
        assertTrue(report.publicPaths().contains("/assets/*"));
    }

    @Test
    void AC_SDK_03_404AtStartupFailsTheWholeStartup() {
        GuanjinConfig cfg = TestConfigs.minimal();
        JwksClient jwks = new JwksClient(URI.create(cfg.resolvedJwksUri()), status(404),
                Duration.ofMinutes(10), true);
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> GuanjinStartupChecker.check(cfg, TestConfigs.prod(), jwks));
        assertEquals(ErrorCodes.KEY_UNAVAILABLE, e.getCode());
        assertTrue(e.getMessage().contains("404"), e.getMessage());
    }

    @Test
    void startupFailsWhenConfigIsInvalid() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getPlatform().setBaseUrl(null);
        JwksClient jwks = new JwksClient(URI.create("http://127.0.0.1:1/.well-known/jwks.json"),
                ok(JWKS_BODY), Duration.ofMinutes(10), true);
        assertThrows(com.guanjin.core.config.GuanjinConfigException.class,
                () -> GuanjinStartupChecker.check(cfg, TestConfigs.prod(), jwks));
    }

    private static HttpFetcher ok(final String body) {
        return new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                return new HttpResult(200, body.getBytes(UTF8), "application/json");
            }
        };
    }

    private static HttpFetcher status(final int status) {
        return new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                return new HttpResult(status, new byte[0], "application/json");
            }
        };
    }
}
