package com.guanjin.spring.boot2;

import com.guanjin.core.authz.GrantedAuthoritiesResolver;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.springframework.security.authentication.UsernamePasswordAuthenticationToken;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;

import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * 身份 → Spring Security {@code Authentication}（契约 GJ-CAP-10 {@code toAuthentication(p)}）。
 *
 * <p>三条硬口径：
 * <ul>
 *   <li><b>principal 里不放断言原文</b>：{@code principal} 就是 {@link GuanjinPrincipal}（只含
 *       sub/uname/roles/acr/amr/cid/jti），{@code credentials} 与 {@code details} 都为 {@code null}——
 *       断言令牌绝不进业务代码、绝不进日志（契约 §3.2 #29：日志不得打完整令牌）。</li>
 *   <li><b>authorities = 角色（加 {@code security.role-prefix}）+ 可选 resolver 的映射结果</b>；
 *       只影响宿主 {@code hasRole} 的用法，**不改变角色事实**（契约 #28）。</li>
 *   <li><b>resolver 抛异常 → 请求失败（fail-closed）</b>，不是"降级成无权限继续跑"。</li>
 * </ul>
 */
public class GuanjinAuthenticationConverter {

    private final String rolePrefix;
    private final GrantedAuthoritiesResolver resolver;

    public GuanjinAuthenticationConverter(String rolePrefix, GrantedAuthoritiesResolver resolver) {
        this.rolePrefix = rolePrefix == null ? "" : rolePrefix;
        this.resolver = resolver;
    }

    /** 契约 GJ-CAP-10：产出一个**已认证**的 {@code Authentication}。 */
    public Authentication toAuthentication(GuanjinPrincipal principal) {
        if (principal == null) {
            throw GuanjinSdkException.tokenInvalid("身份为空：无法构造 Authentication（未认证）");
        }
        List<GrantedAuthority> authorities = new ArrayList<GrantedAuthority>();
        for (GrantedAuthority a : roleAuthorities(principal)) {
            authorities.add(a);
        }
        for (GrantedAuthority a : resolvedAuthorities(principal)) {
            authorities.add(a);
        }
        // credentials = null：断言原文绝不带进来（它不是密码，也不该被业务看到）
        return new UsernamePasswordAuthenticationToken(principal, null, authorities);
    }

    private Set<GrantedAuthority> roleAuthorities(GuanjinPrincipal principal) {
        Set<GrantedAuthority> out = new LinkedHashSet<GrantedAuthority>();
        for (String role : principal.roles()) {
            out.add(new SimpleGrantedAuthority(rolePrefix + role));
        }
        return out;
    }

    private Set<GrantedAuthority> resolvedAuthorities(GuanjinPrincipal principal) {
        Set<GrantedAuthority> out = new LinkedHashSet<GrantedAuthority>();
        if (resolver == null) {
            return out;
        }
        Collection<String> resolved;
        try {
            resolved = resolver.resolve(principal);
        } catch (RuntimeException e) {
            // fail-closed：映射失败即拒绝请求，不得"没有权限就当匿名继续"
            throw GuanjinSdkException.insufficientRole("权限映射失败（resolver 抛异常），拒绝请求");
        }
        if (resolved == null) {
            return out;
        }
        for (String name : resolved) {
            if (name != null && !name.trim().isEmpty()) {
                out.add(new SimpleGrantedAuthority(rolePrefix + name.trim()));
            }
        }
        return out;
    }
}
