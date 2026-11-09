package com.guanjin.spring.boot2;

import com.guanjin.core.config.GuanjinConfigKeys;
import com.guanjin.core.principal.GuanjinContextHolder;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.FilteredClassLoader;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.core.context.SecurityContextHolder;

import javax.servlet.FilterChain;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 契约 #26 的两条合法形态（Spec 裁定 2026-11-10）：
 *
 * <ul>
 *   <li>{@code inject-security-context=true}（默认）而类路径上没有 {@code spring-security-core}
 *       → **启动失败**，报错点名两条修法（"配置说要做、实际没做"不许静默降级）；</li>
 *   <li>{@code false} → **正常启动**（契约 #26 明文允许），身份仍可从 {@code GuanjinContextHolder}
 *       与请求属性取到（"关掉只影响取法、不削弱安全"）；</li>
 *   <li>该 {@code false} **不受 profile 门控、生产也允许**（{@code AC-SDK-02} 只管
 *       {@code enabled} / {@code authz.enabled}）。</li>
 * </ul>
 */
class SecurityContextInjectGuardTest {

    /** 默认配置（true）+ 类路径无 spring-security-core → 启动失败。 */
    @Test
    void missingSpringSecurityWithDefaultTrueFailsStartup() {
        new ApplicationContextRunner()
                .withClassLoader(new FilteredClassLoader(
                        "org.springframework.security.core.context.SecurityContextHolder"))
                .withUserConfiguration(StubBeans.class, GuanjinAutoConfiguration.class)
                .withPropertyValues(baseProps())
                .run(context -> {
                    assertThat(context).hasFailed();
                    String msg = messages(context.getStartupFailure());
                    assertTrue(msg.contains(GuanjinConfigKeys.SECURITY_INJECT_SECURITY_CONTEXT), msg);
                    // 报错必须自解释：点名两条修法
                    assertTrue(msg.contains("spring-security-core"), msg);
                    assertTrue(msg.contains("guanjin.security.inject-security-context=false"), msg);
                });
    }

    /** 显式 false → 正常启动，且身份仍能取到（ContextHolder + 请求属性）。 */
    @Test
    void explicitFalseStartsAndKeepsIdentityAvailable() throws Exception {
        Map<String, Object> props = props();
        props.put("guanjin.security.inject-security-context", "false");
        // 生产 profile：证明这个 false 不受 local|test 门控（replay-store 用 redis 才合法，store 由 StubBeans 提供）
        props.put("guanjin.authz.replay-store", "redis");
        props.put("spring.profiles.active", "prod");

        new ApplicationContextRunner()
                .withClassLoader(new FilteredClassLoader(
                        "org.springframework.security.core.context.SecurityContextHolder"))
                .withUserConfiguration(StubBeans.class, GuanjinAutoConfiguration.class)
                .withPropertyValues(toArray(props))
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    assertThat(context).doesNotHaveBean(SecurityContextInjector.class);

                    // 用注册的 FilterRegistrationBean 拿真实过滤器实例
                    org.springframework.boot.web.servlet.FilterRegistrationBean<?> reg =
                            context.getBean(org.springframework.boot.web.servlet.FilterRegistrationBean.class);
                    GuanjinAssertionFilter real = (GuanjinAssertionFilter) reg.getFilter();

                    final AtomicReference<String> subject = new AtomicReference<String>();
                    final AtomicReference<Object> attribute = new AtomicReference<Object>();
                    FilterChain chain = new FilterChain() {
                        @Override
                        public void doFilter(ServletRequest request, ServletResponse response) {
                            subject.set(GuanjinContextHolder.current() == null
                                    ? null : GuanjinContextHolder.current().subject());
                            attribute.set(request.getAttribute(GuanjinAssertionFilter.PRINCIPAL_ATTRIBUTE));
                        }
                    };
                    org.springframework.mock.web.MockHttpServletRequest req =
                            new org.springframework.mock.web.MockHttpServletRequest("GET", "/orders");
                    req.addHeader("X-GuanJin-Assertion", "stub:jti-nofall");
                    org.springframework.mock.web.MockHttpServletResponse res =
                            new org.springframework.mock.web.MockHttpServletResponse();
                    real.doFilter(req, res, chain);

                    assertEquals(200, res.getStatus(), "关掉 SecurityContext 注入不影响验签与放行");
                    assertEquals("user-1", subject.get(), "GuanjinContextHolder 仍要能取到身份");
                    assertNotNull(attribute.get(), "req.guanjin.principal 属性仍要能取到身份");
                    assertNull(SecurityContextHolder.getContext().getAuthentication(),
                            "没接 Spring Security 时 SecurityContext 本来就为空");
                    assertNull(GuanjinContextHolder.current(), "请求结束必须清理");
                });
    }

    /** 有依赖 + true（默认）→ 注入器是 Spring Security 实现（不退化）。 */
    @Test
    void withSpringSecurityPresentTheInjectorIsTheRealOne() {
        new ApplicationContextRunner()
                .withUserConfiguration(StubBeans.class, GuanjinAutoConfiguration.class)
                .withPropertyValues(baseProps())
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    assertThat(context.getBean(SecurityContextInjector.class))
                            .isInstanceOf(SpringSecurityContextInjector.class);
                });
    }

    private static Map<String, Object> props() {
        Map<String, Object> p = new LinkedHashMap<String, Object>();
        p.put("guanjin.platform.base-url", "http://127.0.0.1:18080");
        p.put("guanjin.client.id", "orders");
        p.put("guanjin.client.hmac-secret", "s3cr3t-test-only");
        p.put("guanjin.authz.replay-store", "memory");
        p.put("spring.profiles.active", "local");
        return p;
    }

    private static String[] baseProps() {
        return toArray(props());
    }

    private static String[] toArray(Map<String, Object> map) {
        String[] out = new String[map.size()];
        int i = 0;
        for (Map.Entry<String, Object> e : map.entrySet()) {
            out[i++] = e.getKey() + "=" + e.getValue();
        }
        return out;
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

    /** 业务方声明：授权模型（#15 默认 true 必需）+ 测试用 JWKS 假平台。 */
    @Configuration(proxyBeanMethods = false)
    static class StubBeans {

        @Bean
        com.guanjin.core.authz.AuthzModel authzModel() {
            return com.guanjin.core.authz.AuthzModel.builder()
                    .role("order_viewer").can("orders", "read")
                    .defaultRole("user").can("profile", "read")
                    .build();
        }

        @Bean
        @org.springframework.context.annotation.Primary
        com.guanjin.core.assertion.AssertionVerifier assertionVerifier(GuanjinProperties properties) {
            return new StubVerifier(properties.getGateway().getIssuer(), properties.resolvedAud());
        }

        @Bean
        @org.springframework.context.annotation.Primary
        com.guanjin.core.replay.ReplayStore replayStore() {
            return new com.guanjin.core.replay.InMemoryReplayStore();
        }

        @Bean
        @org.springframework.context.annotation.Primary
        com.guanjin.core.http.HttpFetcher httpFetcher() {
            final byte[] jwks = ("{\"keys\":[{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"gw-1\","
                    + "\"use\":\"sig\",\"alg\":\"SM3withSM2\",\"x\":\"aa\",\"y\":\"bb\"}]}")
                    .getBytes(java.nio.charset.Charset.forName("UTF-8"));
            return new com.guanjin.core.http.HttpFetcher() {
                @Override
                public com.guanjin.core.http.HttpResult get(java.net.URI uri,
                                                            Map<String, String> headers) {
                    return new com.guanjin.core.http.HttpResult(200, jwks, "application/json");
                }
            };
        }
    }
}
