package com.guanjin.core.authz;

import com.guanjin.core.principal.GuanjinPrincipal;

import java.util.Collection;

/**
 * 业务方的权限映射扩展点（契约 GJ-CAP-11 {@code GrantedAuthoritiesResolver.resolve(p)}，**可选**能力）。
 *
 * <p>职责边界（契约 §3.2 #27 安全约束）：**只做「身份属性 → 业务权限」映射**，
 * 不得在此做放行决策、不得回源平台；**抛异常 → 请求失败（fail-closed）**。
 */
public interface GrantedAuthoritiesResolver {

    /** 返回业务权限名（不含角色前缀；前缀由 {@code security.role-prefix} 统一加）。 */
    Collection<String> resolve(GuanjinPrincipal principal);
}
