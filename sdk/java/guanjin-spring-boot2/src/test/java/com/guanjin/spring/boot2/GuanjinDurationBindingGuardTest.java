package com.guanjin.spring.boot2;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.Banner;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.WebApplicationType;
import org.springframework.context.ConfigurableApplicationContext;

import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 契约 §3.2.1 的**绑定期守卫**（判据 `AC-SDK-11`）：Spring 的 {@code Duration} 默认转换器接受纯数字
 * （按毫秒）、{@code d}（按天）、ISO-8601 {@code PT60S}——这些形态在**绑定之前**必须被拒，
 * 否则同一份配置「Java 起得来、Go 拒」= 静默不兼容。
 *
 * <p>这里起**真的 Spring 容器** + 真的 HTTP 假平台（启动自检会拉 JWKS），不是 mock：
 * 只有真的走完配置绑定链路，才能证明"转换器不再自己发明单位"。
 */
class GuanjinDurationBindingGuardTest {

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
    void AC_SDK_11_bareNumberIsRejectedBeforeBinding() {
        // 裸数字在 Java 侧曾被当成毫秒（隐式单位）；YAML 的 `connect-timeout: 3000` 就是这种形态
        assertRejected("guanjin.platform.connect-timeout", "3000", "guanjin.platform.connect-timeout");
    }

    @Test
    void AC_SDK_11_dayUnitIsRejected() {
        assertRejected("guanjin.gateway.jwks-cache-ttl", "1d", "guanjin.gateway.jwks-cache-ttl");
    }

    @Test
    void AC_SDK_11_iso8601IsRejected() {
        assertRejected("guanjin.gateway.clock-skew", "PT60S", "guanjin.gateway.clock-skew");
    }

    @Test
    void AC_SDK_11_compositeAndFractionFormsAreRejected() {
        assertRejected("guanjin.agent.pull.interval", "1h30m", "guanjin.agent.pull.interval");
        assertRejected("guanjin.gateway.replay-window", "1.5s", "guanjin.gateway.replay-window");
        assertRejected("guanjin.platform.read-timeout", "100ns", "guanjin.platform.read-timeout");
    }

    @Test
    void AC_SDK_11_relaxedCamelCaseNameIsAlsoChecked() {
        // Spring 宽松绑定名（camelCase）与规范 kebab 名必须同样被挡
        assertRejected("guanjin.platform.connectTimeout", "3000", "guanjin.platform.connect-timeout");
    }

    @Test
    void AC_SDK_11_nonStringValueIsRejected() {
        // YAML `connect-timeout: 3000` 会给出 Integer；裸数字不按毫秒解释
        Map<String, Object> props = baseProps();
        props.put("guanjin.platform.connect-timeout", Integer.valueOf(3000));
        Throwable e = assertThrows(Throwable.class, () -> start(props));
        String msg = messages(e);
        assertTrue(msg.contains("guanjin.platform.connect-timeout"), "错误必须报键名：" + msg);
        assertTrue(msg.contains("§3.2.1"), "错误必须指向契约 §3.2.1：" + msg);
        assertTrue(msg.contains("非字符串"), "错误应说明必须是字符串形态：" + msg);
    }

    @Test
    void AC_SDK_11_canonicalFormsStart() {
        Map<String, Object> props = baseProps();
        props.put("guanjin.platform.connect-timeout", "2s");
        props.put("guanjin.platform.read-timeout", "3s");
        props.put("guanjin.gateway.clock-skew", "60s");
        props.put("guanjin.gateway.replay-window", "60s");
        props.put("guanjin.gateway.jwks-cache-ttl", "10m");
        props.put("guanjin.agent.pull.interval", "1h");
        props.put("guanjin.security.role-prefix", "ROLE_");
        props.put("guanjin.authz.enabled", "false"); // local profile 允许；本用例只关心时长解析

        ConfigurableApplicationContext ctx = start(props);
        try {
            GuanjinProperties bound = ctx.getBean(GuanjinProperties.class);
            assertNotNull(bound);
            assertEquals(java.time.Duration.ofSeconds(2), bound.getPlatform().getConnectTimeout());
            assertEquals(java.time.Duration.ofHours(1), bound.getAgent().getPull().getInterval());
        } finally {
            ctx.close();
        }
    }

    @Test
    void AC_SDK_11_surroundingWhitespaceIsTrimmedNotRejected() {
        // 契约 §3.2.1 显式允许「首尾空白（trim）」；Spring 的 Duration 转换器本身不 trim，
        // 所以守卫必须在判过之后把规范文本回写到属性源，否则契约允许的形态在主路径上起不来。
        Map<String, Object> props = baseProps();
        props.put("guanjin.platform.connect-timeout", " 2s ");
        props.put("guanjin.gateway.clock-skew", "60s\n");
        props.put("guanjin.agent.pull.interval", "\t1h\t");
        props.put("guanjin.authz.enabled", "false");

        ConfigurableApplicationContext ctx = start(props);
        try {
            GuanjinProperties bound = ctx.getBean(GuanjinProperties.class);
            assertEquals(java.time.Duration.ofSeconds(2), bound.getPlatform().getConnectTimeout(),
                    "首尾空白必须按 trim 后解析（Go 侧 strings.TrimSpace 同样接受）");
            assertEquals(java.time.Duration.ofSeconds(60), bound.getGateway().getClockSkew());
            assertEquals(java.time.Duration.ofHours(1), bound.getAgent().getPull().getInterval());
        } finally {
            ctx.close();
        }
    }

    /** 该键的原始文本是非法形态 → 启动失败，错误里必须有键名与 §3.2.1 的合法形态。 */
    private void assertRejected(String key, String raw, String expectedKeyInMessage) {
        Map<String, Object> props = baseProps();
        props.put(key, raw);
        Throwable e = assertThrows(Throwable.class, () -> start(props),
                key + "=" + raw + " 必须在绑定前失败（§3.2.1）");
        String msg = messages(e);
        assertTrue(msg.contains(expectedKeyInMessage), "错误必须报出键名：" + msg);
        assertTrue(msg.contains("§3.2.1"), "错误必须指向契约 §3.2.1：" + msg);
        assertTrue(msg.contains("^[0-9]+(ms|s|m|h)$"), "错误应给出唯一合法形态：" + msg);
    }

    private Map<String, Object> baseProps() {
        Map<String, Object> props = new LinkedHashMap<String, Object>();
        props.put("guanjin.platform.base-url", platform.baseUrl());
        props.put("guanjin.client.id", "orders");
        props.put("guanjin.client.hmac-secret", "s3cr3t-test-only");
        props.put("guanjin.authz.replay-store", "memory"); // 单元测试不连 Redis
        props.put("spring.profiles.active", "local");
        return props;
    }

    private ConfigurableApplicationContext start(Map<String, Object> props) {
        SpringApplication app = new SpringApplication(GuanjinAutoConfiguration.class);
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
}
