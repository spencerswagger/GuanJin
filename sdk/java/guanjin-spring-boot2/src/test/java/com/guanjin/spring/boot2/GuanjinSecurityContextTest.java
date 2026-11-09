package com.guanjin.spring.boot2;

import com.guanjin.core.authz.GrantedAuthoritiesResolver;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.context.SecurityContextHolder;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collection;
import java.util.Collections;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-08 第 ④ 步（注入上下文）+ GJ-CAP-10/11 的 Spring 落点。
 *
 * <p>三条断言：① {@code Authentication} 的 principal 是 {@link GuanjinPrincipal}，
 * **断言原文（token）不在任何字段里**；② authorities = 角色（加前缀）+ 可选 resolver 的映射；
 * ③ 未认证时是 **401**（`TOKEN_INVALID`），不是 500/匿名。
 */
class GuanjinSecurityContextTest {

    @AfterEach
    void tearDown() {
        SecurityContextHolder.clearContext();
        GuanjinContextHolder.clear();
    }

    @Test
    void AC_SDK_08_authenticationCarriesPrincipalAndRoleAuthorities() {
        GuanjinAuthenticationConverter converter = new GuanjinAuthenticationConverter("ROLE_", null);
        Authentication auth = converter.toAuthentication(principal("user", "auditor"));

        assertTrue(auth.isAuthenticated(), "已验签身份必须产出已认证的 Authentication");
        assertTrue(auth.getPrincipal() instanceof GuanjinPrincipal);
        assertEquals("user-1", ((GuanjinPrincipal) auth.getPrincipal()).subject());
        assertNull(auth.getCredentials(), "credentials 必须为 null（断言原文绝不带进来）");
        assertNull(auth.getDetails(), "details 也不放令牌");

        List<String> authorities = names(auth.getAuthorities());
        assertTrue(authorities.contains("ROLE_user"), authorities.toString());
        assertTrue(authorities.contains("ROLE_auditor"), authorities.toString());
    }

    @Test
    void AC_SDK_08_assertionTokenNeverReachesPrincipalOrAuthorities() {
        String token = "stub:jti-should-not-leak";
        GuanjinAuthenticationConverter converter = new GuanjinAuthenticationConverter("ROLE_", null);
        Authentication auth = converter.toAuthentication(principal("user"));
        // 把 token 塞进一个"最坏情况"的检查：principal 的任何可见输出都不得含它
        String surface = String.valueOf(auth.getPrincipal()) + auth.getPrincipal().toString()
                + names(auth.getAuthorities()) + auth.getName();
        assertFalse(surface.contains(token), "principal/authorities 不得含断言原文");
        assertFalse(surface.contains("stub:"), "也不得含断言的任何片段");
    }

    @Test
    void customRolePrefixIsApplied() {
        GuanjinAuthenticationConverter converter = new GuanjinAuthenticationConverter("APP_", null);
        Authentication auth = converter.toAuthentication(principal("user"));
        assertTrue(names(auth.getAuthorities()).contains("APP_user"), names(auth.getAuthorities()).toString());
    }

    @Test
    void resolverAddsBusinessAuthoritiesAndFailsClosed() {
        GrantedAuthoritiesResolver ok = new GrantedAuthoritiesResolver() {
            @Override
            public Collection<String> resolve(GuanjinPrincipal p) {
                return Arrays.asList("orders:read", "orders:write");
            }
        };
        Authentication auth = new GuanjinAuthenticationConverter("ROLE_", ok).toAuthentication(principal("user"));
        List<String> names = names(auth.getAuthorities());
        assertTrue(names.contains("ROLE_orders:read"), names.toString());
        assertTrue(names.contains("ROLE_user"), names.toString());

        GrantedAuthoritiesResolver boom = new GrantedAuthoritiesResolver() {
            @Override
            public Collection<String> resolve(GuanjinPrincipal p) {
                throw new IllegalStateException("业务映射炸了");
            }
        };
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> new GuanjinAuthenticationConverter("ROLE_", boom).toAuthentication(principal("user")));
        assertEquals(ErrorCodes.INSUFFICIENT_ROLE, e.getCode(), "resolver 异常必须 fail-closed");
        assertEquals(403, e.getStatus());
    }

    @Test
    void injectorWritesAndClearsSecurityContext() {
        SpringSecurityContextInjector injector =
                new SpringSecurityContextInjector(new GuanjinAuthenticationConverter("ROLE_", null));
        assertTrue(injector.isSpringSecurity());
        injector.inject(principal("user"));
        Authentication auth = SecurityContextHolder.getContext().getAuthentication();
        assertNotNull(auth, "注入后 SecurityContext 必须能取到 Authentication");
        assertEquals("user-1", ((GuanjinPrincipal) auth.getPrincipal()).subject());

        injector.clear();
        assertNull(SecurityContextHolder.getContext().getAuthentication(), "请求结束必须清理（防身份串号）");
    }

    @Test
    void unauthenticatedIs401Not500() {
        // 没有任何注入 → 取身份得到 null（公开路径正常情形）
        assertNull(GuanjinSecurityContext.currentPrincipal());
        // 需要身份的调用点 → 401，不是 NPE/500
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                GuanjinSecurityContext::requirePrincipal);
        assertEquals(401, e.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
    }

    @Test
    void fallbackToContextHolderWhenSpringSecurityDisabled() {
        GuanjinContextHolder.set(principal("user"));
        assertNull(SecurityContextHolder.getContext().getAuthentication(), "未注入 SecurityContext");
        assertEquals("user-1", GuanjinSecurityContext.currentPrincipal().subject(),
                "inject-security-context=false 时仍要能取到身份（契约 #26）");
    }

    @Test
    void filterInjectsSecurityContextThenClearsIt() throws Exception {
        GuanjinProperties props = new GuanjinProperties();
        props.getPlatform().setBaseUrl("http://127.0.0.1:18080");
        props.getClient().setId("orders");
        props.getClient().setHmacSecret("s3cr3t");

        SpringSecurityContextInjector injector =
                new SpringSecurityContextInjector(new GuanjinAuthenticationConverter("ROLE_", null));
        GuanjinAssertionFilter filter = new GuanjinAssertionFilter(props,
                new StubVerifier(props.getGateway().getIssuer(), props.resolvedAud()),
                new com.guanjin.core.replay.ReplayGuard(
                        new com.guanjin.core.replay.InMemoryReplayStore(), java.time.Duration.ofSeconds(60)),
                new com.guanjin.core.publicpath.PathMatcher(props.getPublicPaths(),
                        props.isPublicPathsIncludeBuiltin()),
                injector);

        final java.util.concurrent.atomic.AtomicReference<String> subjectInBusiness =
                new java.util.concurrent.atomic.AtomicReference<String>();
        final java.util.concurrent.atomic.AtomicReference<Boolean> hasRoleInBusiness =
                new java.util.concurrent.atomic.AtomicReference<Boolean>(Boolean.FALSE);
        javax.servlet.FilterChain chain = new javax.servlet.FilterChain() {
            @Override
            public void doFilter(javax.servlet.ServletRequest request, javax.servlet.ServletResponse response) {
                // 业务代码的典型取法（两处都要能取到）
                subjectInBusiness.set(GuanjinSecurityContext.requirePrincipal().subject());
                org.springframework.security.core.Authentication auth =
                        SecurityContextHolder.getContext().getAuthentication();
                for (org.springframework.security.core.GrantedAuthority a : auth.getAuthorities()) {
                    if ("ROLE_user".equals(a.getAuthority())) {
                        hasRoleInBusiness.set(Boolean.TRUE);
                    }
                }
            }
        };
        org.springframework.mock.web.MockHttpServletRequest req =
                new org.springframework.mock.web.MockHttpServletRequest("GET", "/orders");
        req.addHeader("X-GuanJin-Assertion", "stub:jti-sec");
        org.springframework.mock.web.MockHttpServletResponse res =
                new org.springframework.mock.web.MockHttpServletResponse();
        filter.doFilter(req, res, chain);

        assertEquals(200, res.getStatus());
        assertEquals("user-1", subjectInBusiness.get(), "业务代码必须能从 SecurityContext 取到身份");
        assertTrue(hasRoleInBusiness.get(), "authorities 必须含 ROLE_user");
        assertNull(SecurityContextHolder.getContext().getAuthentication(), "请求结束必须清理");
        assertNull(GuanjinContextHolder.current(), "ThreadLocal 也必须清理");
    }

    private static List<String> names(Collection<? extends GrantedAuthority> authorities) {
        List<String> out = new ArrayList<String>();
        for (GrantedAuthority a : authorities) {
            out.add(a.getAuthority());
        }
        return out;
    }

    private static GuanjinPrincipal principal(String... roles) {
        return new GuanjinPrincipal("user-1", "alice", Arrays.asList(roles), "AAL2",
                Arrays.asList("pwd"), "orders", "jti-1");
    }
}
