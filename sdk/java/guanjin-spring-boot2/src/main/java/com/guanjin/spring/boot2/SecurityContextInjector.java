package com.guanjin.spring.boot2;

import com.guanjin.core.principal.GuanjinPrincipal;

/**
 * 宿主身份上下文的注入点（契约 GJ-CAP-10 的 Spring 落点）。
 *
 * <p>抽成接口的原因：{@code spring-security-core} 在 SDK 里是 **optional** 依赖，
 * 宿主没引 Spring Security 时 SDK 不能因此起不来——那种情况用 {@link #NOOP}。
 */
public interface SecurityContextInjector {

    /** 把已验签的身份放进宿主上下文。 */
    void inject(GuanjinPrincipal principal);

    /** 请求结束必须清理（线程复用下不清理 = 身份串号）。 */
    void clear();

    /** 是否真的接了 Spring Security（排障/启动日志用）。 */
    boolean isSpringSecurity();

    /** 没有 Spring Security 时的空实现：身份仍可通过 {@code GuanjinContextHolder} 取到。 */
    SecurityContextInjector NOOP = new SecurityContextInjector() {
        @Override
        public void inject(GuanjinPrincipal principal) {
            // 无宿主上下文可写：GuanjinContextHolder 已由过滤器写入
        }

        @Override
        public void clear() {
            // 无状态可清
        }

        @Override
        public boolean isSpringSecurity() {
            return false;
        }
    };
}
