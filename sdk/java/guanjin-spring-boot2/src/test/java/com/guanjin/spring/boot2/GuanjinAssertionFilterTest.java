package com.guanjin.spring.boot2;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.InMemoryReplayStore;
import com.guanjin.core.replay.ReplayGuard;
import com.guanjin.core.replay.ReplayStore;
import com.guanjin.core.replay.ReplayStoreUnavailableException;
import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;

import javax.servlet.FilterChain;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import java.time.Duration;
import java.util.Collections;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 过滤器链路（契约 D10 六步）：AC-SDK-08 的"剥离/注入"断言 + AC-DP-08 的 SDK 半边（同 jti 二次 → 401）。
 *
 * <p>SM2 验签本身属后续切片，这里用 {@link StubVerifier} 驱动；**剥离、claim、重放、上下文、错误映射
 * 全部是真代码**。真 Redis 的版本见 {@code RedisReplayFilterIT}。
 */
class GuanjinAssertionFilterTest {

    private static final String HEADER = "X-GuanJin-Assertion";

    private GuanjinProperties props() {
        GuanjinProperties p = new GuanjinProperties();
        p.getPlatform().setBaseUrl("http://127.0.0.1:18080");
        p.getClient().setId("orders");
        p.getClient().setHmacSecret("s3cr3t");
        return p;
    }

    private GuanjinAssertionFilter filter(GuanjinProperties p, ReplayStore store) {
        return new GuanjinAssertionFilter(p,
                new StubVerifier(p.getGateway().getIssuer(), p.resolvedAud()),
                new ReplayGuard(store, Duration.ofSeconds(60)),
                new PathMatcher(p.getPublicPaths(), p.isPublicPathsIncludeBuiltin()));
    }

    @Test
    void AC_SDK_08_handlerCannotSeeAssertionHeaderButPrincipalIsInjected() throws Exception {
        GuanjinProperties p = props();
        GuanjinAssertionFilter f = filter(p, new InMemoryReplayStore());
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, "stub:jti-aaa");
        MockHttpServletResponse res = new MockHttpServletResponse();

        final AtomicReference<String> headerSeenByHandler = new AtomicReference<String>("unset");
        final AtomicReference<Boolean> nameListed = new AtomicReference<Boolean>(Boolean.TRUE);
        final AtomicReference<GuanjinPrincipal> principalInHandler = new AtomicReference<GuanjinPrincipal>();
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                javax.servlet.http.HttpServletRequest r = (javax.servlet.http.HttpServletRequest) request;
                headerSeenByHandler.set(r.getHeader(HEADER));
                nameListed.set(Collections.list(r.getHeaderNames()).contains(HEADER));
                principalInHandler.set(GuanjinContextHolder.current());
            }
        };
        f.doFilter(req, res, chain);

        assertEquals(200, res.getStatus());
        assertNull(headerSeenByHandler.get(), "handler 的 getHeader 必须为 null（替换请求视图，不是删头）");
        assertFalse(nameListed.get(), "getHeaderNames 不得再含该头");
        assertNotNull(principalInHandler.get(), "④ 必须注入上下文");
        assertEquals("alice", principalInHandler.get().username());
        assertNull(GuanjinContextHolder.current(), "finally 必须清理 ThreadLocal（防身份串号）");
    }

    @Test
    void AC_DP_08_sameJtiTwiceIs401TokenInvalid() throws Exception {
        GuanjinProperties p = props();
        GuanjinAssertionFilter f = filter(p, new InMemoryReplayStore());

        MockHttpServletResponse first = new MockHttpServletResponse();
        f.doFilter(request("stub:jti-dup"), first, new MockFilterChain());
        assertEquals(200, first.getStatus());

        MockHttpServletResponse second = new MockHttpServletResponse();
        f.doFilter(request("stub:jti-dup"), second, new MockFilterChain());
        assertEquals(401, second.getStatus(), "同 jti 二次必须 401");
        JsonNode body = new ObjectMapper().readTree(second.getContentAsString());
        assertEquals(ErrorCodes.TOKEN_INVALID, body.get("code").asText());
        assertTrue(body.get("message").asText().contains("重放"), body.toString());
    }

    @Test
    void missingHeaderIs401() throws Exception {
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter(props(), new InMemoryReplayStore()).doFilter(
                new MockHttpServletRequest("GET", "/orders"), res, new MockFilterChain());
        assertEquals(401, res.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID,
                new ObjectMapper().readTree(res.getContentAsString()).get("code").asText());
    }

    @Test
    void tamperedTokenIs401AndNothingIsInjected() throws Exception {
        final AtomicReference<Boolean> chainCalled = new AtomicReference<Boolean>(Boolean.FALSE);
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                chainCalled.set(Boolean.TRUE);
            }
        };
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, "tampered.jws.value");
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter(props(), new InMemoryReplayStore()).doFilter(req, res, chain);
        assertEquals(401, res.getStatus());
        assertFalse(chainCalled.get(), "验签失败不得进入业务 handler");
        assertNull(GuanjinContextHolder.current());
    }

    @Test
    void claimMismatchIs401() throws Exception {
        for (String token : new String[]{"stub:jti-iss:badiss", "stub:jti-aud:badaud"}) {
            MockHttpServletResponse res = new MockHttpServletResponse();
            MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
            req.addHeader(HEADER, token);
            filter(props(), new InMemoryReplayStore()).doFilter(req, res, new MockFilterChain());
            assertEquals(401, res.getStatus(), token);
        }
    }

    @Test
    void replayStoreOutageIs503AndNeverPassesThrough() throws Exception {
        ReplayStore broken = new ReplayStore() {
            @Override
            public boolean firstUse(String jti, Duration window) {
                throw new ReplayStoreUnavailableException("connection refused", null);
            }

            @Override
            public String name() {
                return "redis";
            }
        };
        final AtomicReference<Boolean> chainCalled = new AtomicReference<Boolean>(Boolean.FALSE);
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                chainCalled.set(Boolean.TRUE);
            }
        };
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter(props(), broken).doFilter(request("stub:jti-x"), res, chain);
        assertEquals(503, res.getStatus());
        assertEquals(ErrorCodes.REDIS_UNAVAILABLE,
                new ObjectMapper().readTree(res.getContentAsString()).get("code").asText());
        assertFalse(chainCalled.get(), "存储不可用必须拒请求（fail-closed）");
    }

    @Test
    void publicPathBypassesAssertionAndKeepsHeader() throws Exception {
        GuanjinProperties p = props();
        p.setPublicPaths(Collections.singletonList("/openapi.json"));
        final AtomicReference<String> seen = new AtomicReference<String>();
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                seen.set(((javax.servlet.http.HttpServletRequest) request).getHeader(HEADER));
            }
        };
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/openapi.json");
        req.addHeader(HEADER, "anything");
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter(p, new InMemoryReplayStore()).doFilter(req, res, chain);
        assertEquals(200, res.getStatus());
        assertEquals("anything", seen.get(), "公开路径不做剥离（它本来就不该带凭证）");
    }

    @Test
    void contextPathIsStrippedBeforePublicPathMatch() throws Exception {
        GuanjinProperties p = props();
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/app/healthz");
        req.setContextPath("/app");
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter(p, new InMemoryReplayStore()).doFilter(req, res, new MockFilterChain());
        assertEquals(200, res.getStatus(), "内置白名单 /healthz 在 context-path 下也必须命中");
    }

    private static MockHttpServletRequest request(String token) {
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, token);
        return req;
    }
}
