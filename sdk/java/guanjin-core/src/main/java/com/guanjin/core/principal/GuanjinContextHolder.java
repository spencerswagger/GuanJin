package com.guanjin.core.principal;

/**
 * 当前请求的身份上下文（GJ-CAP-10 {@code InjectHostContext} 的"非 Spring 落点"）。
 *
 * <p>Spring 适配层额外把身份放进 {@code SecurityContext}（{@code inject-security-context} 默认 true）；
 * 这里保证**不依赖 Spring 也能取身份**，也保证 {@code inject-security-context=false} 时业务仍可读到。
 *
 * <p>实现方必须在 {@code finally} 里 {@link #clear()}：线程复用（servlet 容器）下不清理 =
 * 身份串号，属于最严重的一类漏洞。
 */
public final class GuanjinContextHolder {

    private static final ThreadLocal<GuanjinPrincipal> CURRENT = new ThreadLocal<GuanjinPrincipal>();

    private GuanjinContextHolder() {
    }

    /** 当前身份；未认证（公开路径）时为 {@code null}。 */
    public static GuanjinPrincipal current() {
        return CURRENT.get();
    }

    public static void set(GuanjinPrincipal principal) {
        CURRENT.set(principal);
    }

    public static void clear() {
        CURRENT.remove();
    }
}
