package com.guanjin.spring.boot2;

import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;

/**
 * 业务侧取身份的唯一入口（契约 GJ-CAP-10 的读取面）。
 *
 * <p>两种落点都支持，优先级：Spring Security {@code SecurityContext} → {@code GuanjinContextHolder}。
 * 这样 {@code inject-security-context=false} 的接入方也能拿到身份（契约 #26：关掉只影响取法，
 * 不跳过验签、不影响判定）。
 *
 * <p>**未认证时是 401 而不是 500**：{@link #requirePrincipal()} 抛的是带 {@code TOKEN_INVALID}
 * 的 SDK 异常（401），不是 {@code NullPointerException}——业务代码不用自己判空，也不会把
 * "没登录"变成 500。
 */
public final class GuanjinSecurityContext {

    private GuanjinSecurityContext() {
    }

    /** 当前身份；未认证返回 {@code null}（公开路径就是这种情形，不是错误）。 */
    public static GuanjinPrincipal currentPrincipal() {
        Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
        if (authentication != null && authentication.getPrincipal() instanceof GuanjinPrincipal) {
            return (GuanjinPrincipal) authentication.getPrincipal();
        }
        return GuanjinContextHolder.current();
    }

    /**
     * 需要身份的调用点用这个：未认证即 401 {@code TOKEN_INVALID}（不是 500、不是匿名放行）。
     */
    public static GuanjinPrincipal requirePrincipal() {
        GuanjinPrincipal p = currentPrincipal();
        if (p == null) {
            throw GuanjinSdkException.tokenInvalid("未认证：当前请求没有可用的身份（401，不是 500）");
        }
        return p;
    }
}
