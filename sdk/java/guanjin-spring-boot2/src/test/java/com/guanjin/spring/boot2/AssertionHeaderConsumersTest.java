package com.guanjin.spring.boot2;

import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.InMemoryReplayStore;
import com.guanjin.core.replay.ReplayGuard;
import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockFilterChain;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;
import javax.servlet.RequestDispatcher;

import javax.servlet.FilterChain;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import javax.servlet.http.HttpServletRequest;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-08 的**三处消费者**断言（契约 D10 第 ⑤ 步）：handler、下游转发、日志**都取不到**该头。
 *
 * <p>为什么必须查三处：只在"handler 那一次"删掉是不够的——框架/日志可能已缓存 header 名，
 * 下游转发也可能重新读原始请求。这里对**同一个被替换后的请求视图**跑三种消费者。
 */
class AssertionHeaderConsumersTest {

    private static final String HEADER = "X-GuanJin-Assertion";

    @Test
    void AC_SDK_08_threeConsumersCannotSeeAssertionHeader() throws Exception {
        GuanjinProperties props = props();
        GuanjinAssertionFilter filter = new GuanjinAssertionFilter(props,
                new StubVerifier(props.getGateway().getIssuer(), props.resolvedAud()),
                new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60)),
                new PathMatcher(props.getPublicPaths(), props.isPublicPathsIncludeBuiltin()));

        final List<String> observations = new ArrayList<String>();
        // 下游转发：捕获"交给下游的那个请求对象"，再按下游的方式读头
        final java.util.concurrent.atomic.AtomicReference<HttpServletRequest> forwarded =
                new java.util.concurrent.atomic.AtomicReference<HttpServletRequest>();
        final RequestDispatcher downstream = new RequestDispatcher() {
            @Override
            public void forward(ServletRequest request, ServletResponse response) {
                forwarded.set((HttpServletRequest) request);
            }

            @Override
            public void include(ServletRequest request, ServletResponse response) {
                // 转发场景只关心 forward
            }
        };

        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response)
                    throws java.io.IOException, javax.servlet.ServletException {
                HttpServletRequest r = (HttpServletRequest) request;

                // ① handler（业务代码）
                observations.add("handler.getHeader=" + r.getHeader(HEADER));
                observations.add("handler.getHeaders.size=" + Collections.list(r.getHeaders(HEADER)).size());
                observations.add("handler.headerNames.contains=" + Collections.list(r.getHeaderNames()).contains(HEADER));

                // ② 日志（典型写法：遍历 headerNames 打全量头；断言该名不出现）
                StringBuilder logged = new StringBuilder();
                for (String name : Collections.list(r.getHeaderNames())) {
                    logged.append(name).append(';');
                }
                observations.add("logger.seen=" + logged.toString().toUpperCase().contains("GUANJIN"));

                // ③ 下游转发（转发请求仍必须看不到）
                downstream.forward(r, response);
                HttpServletRequest down = forwarded.get();
                observations.add("forward.getHeader=" + down.getHeader(HEADER));
                observations.add("forward.headerNames.contains="
                        + Collections.list(down.getHeaderNames()).contains(HEADER));
            }
        };

        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, "stub:jti-consumers");
        req.addHeader("X-Request-Id", "req-1");
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter.doFilter(req, res, chain);

        assertEquals(200, res.getStatus());
        assertEquals("handler.getHeader=null", observations.get(0), "① handler 的 getHeader 必须为 null");
        assertEquals("handler.getHeaders.size=0", observations.get(1), "① getHeaders 必须为空");
        assertEquals("handler.headerNames.contains=false", observations.get(2), "① getHeaderNames 不得含该名");
        assertEquals("logger.seen=false", observations.get(3), "② 日志遍历 headerNames 也看不到");
        assertEquals("forward.getHeader=null", observations.get(4), "③ 下游转发同样看不到");
        assertEquals("forward.headerNames.contains=false", observations.get(5), "③ 下游 headerNames 同样不含");

        // 反向：其它头不能被误伤（只剥这一个）
        assertNull(GuanjinContextHolder.current(), "finally 清理上下文");
    }

    @Test
    void otherHeadersSurviveTheWrapper() throws Exception {
        GuanjinProperties props = props();
        GuanjinAssertionFilter filter = new GuanjinAssertionFilter(props,
                new StubVerifier(props.getGateway().getIssuer(), props.resolvedAud()),
                new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60)),
                new PathMatcher(props.getPublicPaths(), props.isPublicPathsIncludeBuiltin()));
        final List<String> seen = new ArrayList<String>();
        FilterChain chain = new FilterChain() {
            @Override
            public void doFilter(ServletRequest request, ServletResponse response) {
                HttpServletRequest r = (HttpServletRequest) request;
                seen.add(r.getHeader("X-Request-Id"));
                seen.add(String.valueOf(r.getIntHeader("X-Count")));
                seen.add(String.valueOf(Collections.list(r.getHeaderNames()).size()));
            }
        };
        MockHttpServletRequest req = new MockHttpServletRequest("GET", "/orders");
        req.addHeader(HEADER, "stub:jti-keep");
        req.addHeader("X-Request-Id", "req-42");
        req.addHeader("X-Count", "7");
        MockHttpServletResponse res = new MockHttpServletResponse();
        filter.doFilter(req, res, chain);

        assertEquals("req-42", seen.get(0), "其它头必须原样保留");
        assertEquals("7", seen.get(1));
        assertEquals("2", seen.get(2), "只剩 X-Request-Id 与 X-Count");
        assertTrue(res.getStatus() == 200);
        assertFalse(seen.contains(HEADER));
    }

    private static GuanjinProperties props() {
        GuanjinProperties p = new GuanjinProperties();
        p.getPlatform().setBaseUrl("http://127.0.0.1:18080");
        p.getClient().setId("orders");
        p.getClient().setHmacSecret("s3cr3t");
        return p;
    }
}
