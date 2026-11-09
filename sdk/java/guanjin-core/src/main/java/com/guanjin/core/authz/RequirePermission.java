package com.guanjin.core.authz;

import java.lang.annotation.Documented;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * 一行保护接口（契约 GJ-CAP-14 {@code @RequirePermission(resource, action)}）。
 *
 * <p>**不提供** {@code RequireRole}：按角色放行会让"角色"变成绕过资源/动作判定的后门
 * （契约 §2.1 明写）。
 */
@Documented
@Retention(RetentionPolicy.RUNTIME)
@Target({ElementType.METHOD, ElementType.TYPE})
public @interface RequirePermission {

    /** 资源名（与 {@code AuthzModel.role(...).can(resource, ...)} 声明的一致）。 */
    String resource();

    /** 动作名（同上；{@code "*"} 只用于显式声明的全动作策略）。 */
    String action();
}
