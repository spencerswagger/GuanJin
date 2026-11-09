package com.guanjin.core.error;

/**
 * 带对外码的 SDK 异常：{@code code}（接口清单 §5.4 字面码）+ HTTP 状态 + 面向人的说明。
 *
 * <p>fail-closed 的三个出口（契约 §4.4）：
 * <ul>
 *   <li>401 {@code TOKEN_INVALID} —— 断言不可信（含 {@code jti} 重放）；</li>
 *   <li>503 {@code KEY_UNAVAILABLE} —— JWKS 不可得且无缓存；</li>
 *   <li>503 {@code REDIS_UNAVAILABLE} —— {@code jti} 存储不可用。</li>
 * </ul>
 *
 * <p>没有"放行"这个出口：所有构造器都对应拒绝。
 */
public class GuanjinSdkException extends RuntimeException {

    private static final long serialVersionUID = 1L;

    private final String code;
    private final int status;

    public GuanjinSdkException(String code, int status, String message, Throwable cause) {
        super(code + ": " + message, cause);
        this.code = code;
        this.status = status;
    }

    public String getCode() {
        return code;
    }

    public int getStatus() {
        return status;
    }

    /** 401 {@code TOKEN_INVALID}（缺头 / 验签失败 / claim 越界 / jti 重放 / iss·aud 不符）。 */
    public static GuanjinSdkException tokenInvalid(String message) {
        return new GuanjinSdkException(ErrorCodes.TOKEN_INVALID, 401, message, null);
    }

    /** 401 {@code TOKEN_INVALID}，带底层原因（只进日志）。 */
    public static GuanjinSdkException tokenInvalid(String message, Throwable cause) {
        return new GuanjinSdkException(ErrorCodes.TOKEN_INVALID, 401, message, cause);
    }

    /** 503 {@code KEY_UNAVAILABLE}（不得用 DEPENDENCY_UNAVAILABLE / SERVICE_DEGRADED）。 */
    public static GuanjinSdkException keyUnavailable(String message, Throwable cause) {
        return new GuanjinSdkException(ErrorCodes.KEY_UNAVAILABLE, 503, message, cause);
    }

    /** 503 {@code REDIS_UNAVAILABLE}（jti 存储不可用 → 拒请求）。 */
    public static GuanjinSdkException redisUnavailable(String message, Throwable cause) {
        return new GuanjinSdkException(ErrorCodes.REDIS_UNAVAILABLE, 503, message, cause);
    }

    /** 403 {@code INSUFFICIENT_ROLE}（本地判定未通过 / 模型完整性校验失败）。 */
    public static GuanjinSdkException insufficientRole(String message) {
        return new GuanjinSdkException(ErrorCodes.INSUFFICIENT_ROLE, 403, message, null);
    }
}
