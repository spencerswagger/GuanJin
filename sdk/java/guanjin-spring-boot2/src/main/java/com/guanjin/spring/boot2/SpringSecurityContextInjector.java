package com.guanjin.spring.boot2;

import com.guanjin.core.principal.GuanjinPrincipal;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContext;
import org.springframework.security.core.context.SecurityContextHolder;

/**
 * 把身份写进 Spring Security 的 {@code SecurityContext}（契约 GJ-CAP-10）。
 *
 * <p>只在 {@code guanjin.security.inject-security-context=true}（默认）且宿主引了
 * Spring Security 时生效；{@code false} 只影响宿主取身份的方式，**不跳过验签、不影响判定**（契约 #26）。
 */
public class SpringSecurityContextInjector implements SecurityContextInjector {

    private final GuanjinAuthenticationConverter converter;

    public SpringSecurityContextInjector(GuanjinAuthenticationConverter converter) {
        if (converter == null) {
            throw new IllegalArgumentException("GuanjinAuthenticationConverter 不能为 null");
        }
        this.converter = converter;
    }

    @Override
    public void inject(GuanjinPrincipal principal) {
        Authentication authentication = converter.toAuthentication(principal);
        SecurityContext context = SecurityContextHolder.createEmptyContext();
        context.setAuthentication(authentication);
        SecurityContextHolder.setContext(context);
    }

    @Override
    public void clear() {
        // 清空而不是 remove()：保持"有 context 但未认证"的语义，避免下游 NPE
        SecurityContextHolder.clearContext();
    }

    @Override
    public boolean isSpringSecurity() {
        return true;
    }
}
