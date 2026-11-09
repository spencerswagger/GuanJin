package com.guanjin.spring.boot2;

import com.guanjin.core.agent.PullRequestSigner;
import com.guanjin.core.assertion.AssertionVerifier;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.Banner;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.WebApplicationType;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;

import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-10②：{@code agent.pull.enabled} 默认 {@code false} → **启动后 30s 内零次**
 * {@code /api/v1/agent/policies}（对齐索引 §4.2-24「零回源」）。
 *
 * <p>计数来自**真 HTTP 假平台**（不是估算）；同一台假平台还承担启动期的 JWKS 请求，
 * 因此"计数为 0"不是"服务没起来"的假阳性：JWKS 计数必须 ≥ 1。
 *
 * <p>默认配置整条链路都按契约默认值跑：{@code replay-store=redis}（连**真 Redis**）+
 * {@code jwks-uri} 由 base-url 派生 + {@code pull.enabled=false}。
 */
class AgentPullZeroCallIT {

    private static final String POLICIES = "/api/v1/agent/policies";

    private TestPlatform platform;
    private LettuceConnectionFactory redis;

    @BeforeEach
    void setUp() throws IOException {
        platform = new TestPlatform();
        redis = RedisTestSupport.factory(RedisTestSupport.password());
    }

    @AfterEach
    void tearDown() {
        platform.close();
        redis.destroy();
    }

    @Test
    void AC_SDK_10_zeroPolicyCallsWithin30sWithDefaultConfig() throws Exception {
        ConfigurableApplicationContext ctx = start(new LinkedHashMap<String, Object>(), false);
        try {
            assertEquals(1, platform.hits("/.well-known/jwks.json"),
                    "启动自检必须命中真实 JWKS（证明假平台与计数是活的）");
            assertEquals(0, platform.hits(POLICIES), "启动瞬间不得拉取策略");

            Thread.sleep(30_000L); // 契约判据写的是 30s+：真等，不估算

            assertEquals(0, platform.hits(POLICIES),
                    "默认 agent.pull.enabled=false：30s 内 /api/v1/agent/policies 必须零调用");
            assertTrue(platform.hits("/.well-known/jwks.json") >= 1, "JWKS 计数仍应 ≥1");
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SDK_10_control_enabledPullerActuallyCalls() throws Exception {
        Map<String, Object> props = new LinkedHashMap<String, Object>();
        props.put("guanjin.agent.pull.enabled", "true");
        props.put("guanjin.agent.pull.interval", "1s");
        ConfigurableApplicationContext ctx = start(props, true);
        try {
            long deadline = System.currentTimeMillis() + 8_000L;
            while (System.currentTimeMillis() < deadline && platform.hits(POLICIES) == 0) {
                Thread.sleep(200L);
            }
            assertTrue(platform.hits(POLICIES) >= 1,
                    "正控制：显式置 true 后必须真的请求 /api/v1/agent/policies（否则零调用结论不可信）");
        } finally {
            ctx.close();
        }
    }

    private ConfigurableApplicationContext start(Map<String, Object> extra, boolean withSigner) {
        Map<String, Object> props = new LinkedHashMap<String, Object>();
        props.put("guanjin.platform.base-url", platform.baseUrl());
        props.put("guanjin.client.id", "orders");
        props.put("guanjin.client.hmac-secret", "s3cr3t-test-only");
        props.put("spring.profiles.active", "local");
        props.putAll(extra);

        java.util.List<Class<?>> sources = new java.util.ArrayList<Class<?>>();
        sources.add(GuanjinAutoConfiguration.class);
        sources.add(StubBeans.class);
        if (withSigner) {
            sources.add(SignerBean.class);
        }
        SpringApplication app = new SpringApplication(sources.toArray(new Class<?>[0]));
        app.setWebApplicationType(WebApplicationType.NONE);
        app.setBannerMode(Banner.Mode.OFF);
        app.setDefaultProperties(props);
        return app.run();
    }

    /** 验签器（SM2 属后续切片）+ 真 Redis 的 StringRedisTemplate（默认 replay-store=redis 需要它）。 */
    @Configuration(proxyBeanMethods = false)
    static class StubBeans {

        @Bean
        AssertionVerifier assertionVerifier(GuanjinProperties properties) {
            return new StubVerifier(properties.getGateway().getIssuer(), properties.resolvedAud());
        }

        @Bean
        StringRedisTemplate stringRedisTemplate() {
            StringRedisTemplate t = new StringRedisTemplate(
                    RedisTestSupport.factory(RedisTestSupport.password()));
            t.afterPropertiesSet();
            return t;
        }
    }

    /** 正控制用：拉取请求签名器（GJ-CAP-21 的 HMAC 属后续切片）。 */
    @Configuration(proxyBeanMethods = false)
    static class SignerBean {
        @Bean
        PullRequestSigner pullRequestSigner() {
            return new PullRequestSigner() {
                @Override
                public java.util.Map<String, String> headers(String method, String path) {
                    Map<String, String> h = new LinkedHashMap<String, String>();
                    h.put("X-GuanJin-Client", "orders");
                    h.put("X-GuanJin-Signature", "test-only");
                    return h;
                }
            };
        }
    }
}
