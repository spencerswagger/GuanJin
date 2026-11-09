package com.guanjin.core.agent;

import com.guanjin.core.http.HttpFetchException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;
import org.junit.jupiter.api.Test;

import java.net.URI;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** GJ-CAP-21：默认不拉取（AC-SDK-10② 的组件级断言）、不得发未签名请求。 */
class PolicyPullerTest {

    private static final URI POLICIES = URI.create("http://127.0.0.1:18080/api/v1/agent/policies");

    @Test
    void AC_SDK_10_pullerNeverCallsWithoutStart() throws Exception {
        final AtomicInteger calls = new AtomicInteger();
        PolicyPuller puller = new PolicyPuller(POLICIES, Duration.ofSeconds(30), counting(calls), signer());
        // 没 start() = 默认关闭语义：组件在，但一次都不发
        Thread.sleep(1200L);
        assertEquals(0, calls.get(), "未启动的拉取器不得产生任何请求");
        puller.close();
    }

    @Test
    void enabledPullerCallsSignedEndpoint() throws Exception {
        final AtomicInteger calls = new AtomicInteger();
        final Map<String, String> lastHeaders = new LinkedHashMap<String, String>();
        HttpFetcher fetcher = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                calls.incrementAndGet();
                if (headers != null) {
                    lastHeaders.putAll(headers);
                }
                return new HttpResult(200, "{}".getBytes(), "application/json");
            }
        };
        PolicyPuller puller = new PolicyPuller(POLICIES, Duration.ofMillis(100), fetcher, signer());
        puller.start();
        Thread.sleep(450L);
        puller.close();
        assertTrue(calls.get() >= 2, "启用后必须真的请求（正控制），实际=" + calls.get());
        assertTrue(lastHeaders.containsKey("X-Test-Signature"), "必须带签名头：" + lastHeaders);
    }

    @Test
    void unsignedPullerRefusesToExist() {
        IllegalStateException e = assertThrows(IllegalStateException.class,
                () -> new PolicyPuller(POLICIES, Duration.ofSeconds(30), counting(new AtomicInteger()), null));
        assertTrue(e.getMessage().contains("未签名"), e.getMessage());
    }

    @Test
    void pullFailureDoesNotThrowToCaller() {
        HttpFetcher boom = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException {
                throw new HttpFetchException("refused", null);
            }
        };
        PolicyPuller puller = new PolicyPuller(POLICIES, Duration.ofSeconds(30), boom, signer());
        puller.pullOnce(); // 不得抛出（拉取失败不阻塞业务，契约 §4.4）
        assertEquals(1, puller.attempts());
        assertEquals(0, puller.successes());
        assertEquals(-2, puller.lastStatus());
    }

    private static HttpFetcher counting(final AtomicInteger calls) {
        return new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                calls.incrementAndGet();
                return new HttpResult(200, "{}".getBytes(), "application/json");
            }
        };
    }

    private static PullRequestSigner signer() {
        return new PullRequestSigner() {
            @Override
            public Map<String, String> headers(String method, String path) {
                Map<String, String> h = new LinkedHashMap<String, String>();
                h.put("X-Test-Signature", "test-only");
                h.put("X-GuanJin-Client", "orders");
                return h;
            }
        };
    }
}
