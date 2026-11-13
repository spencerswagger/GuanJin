package com.guanjin.spring.boot2;

import com.guanjin.core.assertion.AssertionVerifier;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.Banner;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 启动期守卫：AC-SDK-02（profile 门控）、AC-SDK-03（默认配置首启命中真实 JWKS，404 即失败）、
 * AC-SDK-04（平台地址缺失/非法 → 启动失败）、AC-FC-05（未知/废止键 → 启动失败）。
 *
 * <p>这里起的是**真的 Spring 容器**（WebApplicationType.NONE）+ **真的 HTTP 假平台**，
 * 不是 mock：JWKS 的 200/404 都来自真实 socket 响应。
 */
class GuanjinAutoConfigurationTest {

    private TestPlatform platform;

    @BeforeEach
    void setUp() throws IOException {
        platform = new TestPlatform();
    }

    @AfterEach
    void tearDown() {
        platform.close();
    }

    @Test
    void AC_SDK_03_defaultConfigHitsRealJwksOnStartup() {
        ConfigurableApplicationContext ctx = start(baseProps(), StubConfig.class);
        try {
            GuanjinStartupReport report = ctx.getBean(GuanjinStartupReport.class);
            assertNotNull(report.report());
            assertTrue(report.report().gatewayKids().contains("gw-2026-11"),
                    "启动自检必须拿到网关用途 kid：" + report.report());
            assertEquals(1, platform.hits("/.well-known/jwks.json"), "默认配置首启必须命中一次真实 JWKS");
            assertEquals(1, ctx.getBeansOfType(FilterRegistrationBean.class).size(), "enabled=true 必须注册过滤器");
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SDK_03_jwks404FailsStartup() {
        platform.jwksStatus(404);
        Throwable e = assertThrows(Throwable.class, () -> start(baseProps(), StubConfig.class));
        String msg = messages(e);
        assertTrue(msg.contains("KEY_UNAVAILABLE"), msg);
        assertTrue(msg.contains("404"), msg);
    }

    @Test
    void AC_SDK_03_jwksWithoutGatewayKeyWarnsButStarts() {
        // 只有对外令牌用途的键（alg=SM3withSM2，000013 CHECK 绑死 oidc）：网关键缺席，须告警
        platform.jwksBody("{\"keys\":[{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"oidc-1\",\"use\":\"sig\","
                + "\"alg\":\"SM3withSM2\",\"x\":\"aa\",\"y\":\"bb\"}]}");
        ConfigurableApplicationContext ctx = start(baseProps(), StubConfig.class);
        try {
            GuanjinStartupReport report = ctx.getBean(GuanjinStartupReport.class);
            assertTrue(report.report().gatewayKids().isEmpty());
            assertTrue(report.report().warnings().toString().contains("alg=SM2"),
                    "没有网关用途公钥必须告警（否则任何断言都会 401）");
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SDK_04_missingPlatformUrlFailsStartup() {
        Map<String, Object> props = baseProps();
        props.remove("guanjin.platform.base-url");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        String msg = messages(e);
        assertTrue(msg.contains("guanjin.platform.base-url"), msg);
        assertEquals(0, platform.hits("/.well-known/jwks.json"), "配置非法时不得发起任何请求");
    }

    @Test
    void AC_SDK_04_illegalPlatformUrlFailsStartup() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.platform.base-url", "not-a-url");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("guanjin.platform.base-url"), messages(e));
    }

    @Test
    void AC_SDK_02_enabledFalseInLocalStartsButRegistersNoFilter() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.enabled", "false");
        props.put("spring.profiles.active", "local");
        ConfigurableApplicationContext ctx = start(props, StubConfig.class);
        try {
            assertEquals(0, ctx.getBeansOfType(FilterRegistrationBean.class).size(),
                    "enabled=false（local|test）= 不注册过滤器（等价未接入）");
            assertEquals(0, platform.hits("/.well-known/jwks.json"), "未接入时不拉 JWKS");
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SDK_02_enabledFalseOutsideLocalFailsStartup() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.enabled", "false");
        props.put("spring.profiles.active", "prod");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("guanjin.enabled"), messages(e));
    }

    @Test
    void AC_SDK_02_authzEnabledFalseOutsideLocalFailsStartup() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.authz.enabled", "false");
        props.put("spring.profiles.active", "staging");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("guanjin.authz.enabled"), messages(e));
    }

    @Test
    void AC_SESS_05_memoryReplayStoreOutsideLocalFailsStartup() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.authz.replay-store", "memory");
        props.put("spring.profiles.active", "prod");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("guanjin.authz.replay-store"), messages(e));
    }

    @Test
    void AC_SESS_05_memoryReplayStoreInLocalStarts() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.authz.replay-store", "memory");
        props.put("spring.profiles.active", "local");
        ConfigurableApplicationContext ctx = start(props, StubConfig.class);
        try {
            assertEquals("memory", ctx.getBean(com.guanjin.core.replay.ReplayGuard.class).store().name());
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SESS_05_redisWithoutClientFailsStartupInsteadOfFallingBackToMemory() {
        // 默认 replay-store=redis，但容器里没有任何 StringRedisTemplate（本测试不引入 Redis 配置）
        Map<String, Object> props = baseProps();
        props.remove("guanjin.authz.replay-store");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        String msg = messages(e);
        assertTrue(msg.contains("guanjin.authz.replay-store"), msg);
        assertTrue(msg.contains("StringRedisTemplate"), msg);
    }

    @Test
    void AC_FC_05_unknownKeyFailsStartupWithKeyName() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.foo", "1");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("guanjin.foo"), messages(e));
    }

    @Test
    void AC_SDK_01_modeKeyFailsStartup() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.mode", "direct");
        Throwable e = assertThrows(Throwable.class, () -> start(props, StubConfig.class));
        assertTrue(messages(e).contains("mode"), messages(e));
    }

    @Test
    void defaultVerifierIsRealSm2AndCannotBeAbsent() {
        // GJ-CAP-01 无跳过开关：验签器由自动配置**默认提供**，不存在"没有验签器"的形态
        ConfigurableApplicationContext ctx = start(baseProps(), StubConfig.class);
        try {
            assertTrue(ctx.getBean(AssertionVerifier.class) instanceof com.guanjin.core.assertion.Sm2AssertionVerifier,
                    "默认验签器必须是 SM2 真验签实现，实际=" + ctx.getBean(AssertionVerifier.class).getClass());
        } finally {
            ctx.close();
        }
    }

    @Test
    void authzEnabledWithoutModelRefusesToStart() {
        // authz.enabled=true（默认）但宿主没声明 AuthzModel → 没有策略依据，拒绝启动（不得"没配就全放行"）
        Throwable e = assertThrows(Throwable.class, () -> start(baseProps()));
        String msg = messages(e);
        assertTrue(msg.contains("AuthzModel"), msg);
        // 报错必须**自解释**（Spec 裁定 2026-11-10）：点名「声明 AuthzModel bean」与
        // 「仅 local|test 可置 authz.enabled=false」两条修法，接入方看日志就知道怎么改
        assertTrue(msg.contains("声明 AuthzModel bean"), msg);
        assertTrue(msg.contains("local|test"), msg);
        assertTrue(msg.contains("guanjin.authz.enabled=false"), msg);
    }

    // ---- helpers ----

    private Map<String, Object> baseProps() {
        Map<String, Object> props = new LinkedHashMap<String, Object>();
        props.put("guanjin.platform.base-url", platform.baseUrl());
        props.put("guanjin.client.id", "orders");
        props.put("guanjin.client.hmac-secret", "s3cr3t-test-only");
        props.put("guanjin.authz.replay-store", "memory"); // 单元测试不连 Redis；真 Redis 用例见 *IT
        props.put("spring.profiles.active", "local");
        return props;
    }

    private ConfigurableApplicationContext start(Map<String, Object> props, Class<?>... extra) {
        Class<?>[] sources = new Class<?>[extra.length + 1];
        sources[0] = GuanjinAutoConfiguration.class;
        System.arraycopy(extra, 0, sources, 1, extra.length);
        org.springframework.boot.SpringApplication app =
                new org.springframework.boot.SpringApplication(sources);
        app.setWebApplicationType(WebApplicationType.NONE);
        app.setBannerMode(Banner.Mode.OFF);
        app.setDefaultProperties(props);
        return app.run();
    }

    private static String messages(Throwable e) {
        StringBuilder sb = new StringBuilder();
        Throwable cur = e;
        int guard = 0;
        while (cur != null && guard++ < 20) {
            if (cur.getMessage() != null) {
                sb.append(cur.getMessage()).append(" | ");
            }
            cur = cur.getCause();
        }
        return sb.toString();
    }

    /** 测试专用：业务方声明授权模型（GJ-CAP-13）；验签器由自动配置默认提供（SM2）。 */
    @Configuration(proxyBeanMethods = false)
    static class StubConfig {
        @Bean
        com.guanjin.core.authz.AuthzModel authzModel() {
            return com.guanjin.core.authz.AuthzModel.builder()
                    .role("order_viewer").can("orders", "read")
                    .defaultRole("user").can("profile", "read")
                    .build();
        }
    }
}
