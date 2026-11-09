package com.guanjin.spring.boot2;

import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.security.SecurityProperties;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.core.Ordered;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-08 ④：过滤器**排在 Spring Security 之前**。
 *
 * <p>依据（可机械核）：
 * <ul>
 *   <li>契约 §5.1 接入说明写死顺序 {@code HIGHEST_PRECEDENCE + 100}；</li>
 *   <li>Spring Boot 2.7 把整条 Spring Security 过滤器链
 *       （{@code springSecurityFilterChain}，内含 {@code UsernamePasswordAuthenticationFilter}）
 *       注册在 {@link SecurityProperties#DEFAULT_FILTER_ORDER} = <b>-100</b>；</li>
 *   <li>因此「排它之前」等价于「order 严格小于 -100」。这里直接引用该常量做断言，
 *       不抄一个魔数——Spring Boot 改了这个值，测试会红。</li>
 * </ul>
 */
class GuanjinFilterOrderTest {

    @Test
    void assertionFilterRunsBeforeSpringSecurityChain() {
        assertEquals(Ordered.HIGHEST_PRECEDENCE + 100, GuanjinAssertionFilter.ORDER,
                "契约 §5.1 冻结的过滤器顺序");

        int securityChainOrder = SecurityProperties.DEFAULT_FILTER_ORDER;
        assertEquals(-100, securityChainOrder, "Spring Boot 2.7 的 security 链顺序（常量变了要重新评估）");

        assertTrue(GuanjinAssertionFilter.ORDER < securityChainOrder,
                "断言过滤器必须早于 security 链：our=" + GuanjinAssertionFilter.ORDER
                        + " security=" + securityChainOrder);
        // HIGHEST_PRECEDENCE + 100 = Integer.MIN_VALUE + 100，远小于 -100（不是"碰巧"）
        assertTrue(GuanjinAssertionFilter.ORDER < 0);
    }

    @Test
    void registrationCarriesTheFrozenOrder() {
        FilterRegistrationBean<GuanjinAssertionFilter> reg = new FilterRegistrationBean<GuanjinAssertionFilter>(
                new GuanjinAssertionFilter(new GuanjinProperties(), new StubVerifier("guanjin-gateway", "orders"),
                        new com.guanjin.core.replay.ReplayGuard(new com.guanjin.core.replay.InMemoryReplayStore(),
                                java.time.Duration.ofSeconds(60)),
                        new com.guanjin.core.publicpath.PathMatcher(null, true)));
        reg.setOrder(GuanjinAssertionFilter.ORDER);
        assertEquals(GuanjinAssertionFilter.ORDER, reg.getOrder());
        assertTrue(reg.getOrder() < SecurityProperties.DEFAULT_FILTER_ORDER);
    }
}
