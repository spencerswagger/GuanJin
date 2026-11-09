package com.guanjin.core.error;

/**
 * 对外错误码（契约 §4.1 M4 / §4.4 fail-closed 矩阵）。
 *
 * <p><b>只消费接口清单 §5.4 的字面码</b>：不自造前缀、不做语言化改写（契约 N6）。
 * 三语言逐字符相同。
 */
public final class ErrorCodes {

    private ErrorCodes() {
    }

    /**
     * 内部断言校验失败的**唯一**对外码：缺头 / 验签失败 / claim 越界 / {@code jti} 重放 /
     * {@code iss}·{@code aud} 不符**全部**归它。
     *
     * <p>故意不区分"过期"与"伪造"（契约 §4.4 末表）：区分等于告诉攻击者"这张票只是旧了"，
     * 可诊断性由日志里的 {@code requestId} 承担，不由响应码承担。
     */
    public static final String TOKEN_INVALID = "TOKEN_INVALID";

    /** JWKS 不可得且无本地缓存（验签公钥拿不到 = 无法确认身份）。 */
    public static final String KEY_UNAVAILABLE = "KEY_UNAVAILABLE";

    /** {@code jti} 存储（{@code replay-store=redis}）不可用 → 拒请求，不放行。 */
    public static final String REDIS_UNAVAILABLE = "REDIS_UNAVAILABLE";

    /** 本地判定未通过 / 模型完整性校验失败（本切片未实现判定，仅占位常量）。 */
    public static final String INSUFFICIENT_ROLE = "INSUFFICIENT_ROLE";

    /** 已废弃、SDK **永不返回**（契约 D8-C′⑤）。 */
    public static final String SERVICE_DEGRADED = "SERVICE_DEGRADED";
}
