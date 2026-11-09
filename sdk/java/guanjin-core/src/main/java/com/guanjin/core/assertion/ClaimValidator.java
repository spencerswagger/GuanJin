package com.guanjin.core.assertion;

import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.error.GuanjinSdkException;

import java.time.Duration;
import java.time.Instant;

/**
 * claim 校验（契约 GJ-CAP-05 {@code ValidateExpIat} + GJ-CAP-06 {@code ValidateIssAud}）。
 *
 * <p>口径（契约 §4.1 M4）：任何一条不符 → **401 {@code TOKEN_INVALID}**，不自造码、
 * 不区分"过期"与"伪造"（区分等于告诉攻击者"这张票只是旧了"）。
 */
public final class ClaimValidator {

    private ClaimValidator() {
    }

    /** 用配置里的 {@code clock-skew}（#10）与 {@code iss}/{@code aud}（#8/#9）校验。 */
    public static void validate(Assertion a, GuanjinConfig cfg) {
        validate(a, cfg, Instant.now());
    }

    /** 可注入"当前时刻"（单测用边界值，不靠 sleep）。 */
    public static void validate(Assertion a, GuanjinConfig cfg, Instant now) {
        if (a == null) {
            throw GuanjinSdkException.tokenInvalid("断言缺失");
        }
        Duration skew = cfg.getGateway().getClockSkew();

        if (a.expiresAt() == null) {
            throw GuanjinSdkException.tokenInvalid("断言缺 exp");
        }
        if (a.issuedAt() == null) {
            throw GuanjinSdkException.tokenInvalid("断言缺 iat");
        }
        // exp：过期超过 skew 才算过期（容忍时钟偏移，默认 ±60s）
        if (now.minus(skew).isAfter(a.expiresAt())) {
            throw GuanjinSdkException.tokenInvalid("断言已过期（exp + skew < now）");
        }
        // iat：未来时间超过 skew 即拒
        if (a.issuedAt().isAfter(now.plus(skew))) {
            throw GuanjinSdkException.tokenInvalid("断言 iat 在未来（超出 clock-skew）");
        }

        String expectedIss = cfg.getGateway().getIssuer();
        if (expectedIss == null || !expectedIss.equals(a.issuer())) {
            throw GuanjinSdkException.tokenInvalid("断言 iss 不符（期望 " + expectedIss + "）");
        }
        String expectedAud = cfg.resolvedAud();
        if (expectedAud == null || !expectedAud.equals(a.audience())) {
            throw GuanjinSdkException.tokenInvalid("断言 aud 不符（期望 " + expectedAud + "）");
        }
    }
}
