package com.guanjin.core.replay;

/**
 * {@code jti} 存储不可用（Redis 连不上 / AUTH 失败 / 超时）。
 *
 * <p>这是**拒绝**信号，不是"降级放行"信号：上层必须映射成 503 {@code REDIS_UNAVAILABLE}
 * 并拒掉请求（契约 §4.4；AC-SDK-06）。
 */
public class ReplayStoreUnavailableException extends RuntimeException {

    private static final long serialVersionUID = 1L;

    public ReplayStoreUnavailableException(String message, Throwable cause) {
        super(message, cause);
    }
}
