package com.guanjin.core.assertion;

import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.TestConfigs;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.time.Instant;
import java.util.Arrays;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** GJ-CAP-05/06：exp/iat 偏移、iss、aud；全部映射 401 TOKEN_INVALID（不区分过期与伪造）。 */
class ClaimValidatorTest {

    private static final Instant NOW = Instant.parse("2026-11-09T12:00:00Z");

    private static Assertion assertion(Instant iat, Instant exp, String iss, String aud) {
        return new Assertion("jti-1", iss, aud, exp, iat, "user-1", "alice",
                Arrays.asList("user"), "AAL2", Arrays.asList("pwd"), "orders");
    }

    @Test
    void validAssertionPasses() {
        GuanjinConfig cfg = TestConfigs.minimal();
        ClaimValidator.validate(assertion(NOW, NOW.plusSeconds(60), "guanjin-gateway", "orders"), cfg, NOW);
    }

    @Test
    void AC_SDK_05_expWithinSkewPassesAndBeyondFails() {
        GuanjinConfig cfg = TestConfigs.minimal(); // skew = 60s
        // exp 刚过 4s：仍在 skew 内 → 通过
        ClaimValidator.validate(assertion(NOW.minusSeconds(64), NOW.minusSeconds(4),
                "guanjin-gateway", "orders"), cfg, NOW);
        // exp 过了 61s：超出 skew → 401
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW.minusSeconds(121), NOW.minusSeconds(61),
                        "guanjin-gateway", "orders"), cfg, NOW));
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
        assertEquals(401, e.getStatus());
    }

    @Test
    void AC_SDK_05_iatInFutureBeyondSkewFails() {
        GuanjinConfig cfg = TestConfigs.minimal();
        ClaimValidator.validate(assertion(NOW.plusSeconds(59), NOW.plusSeconds(119),
                "guanjin-gateway", "orders"), cfg, NOW);
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW.plusSeconds(61), NOW.plusSeconds(121),
                        "guanjin-gateway", "orders"), cfg, NOW)).getCode());
    }

    /**
     * 契约 v16 §8.8 E42：`clock-skew=0s` = **零容忍**——exp/iat 按严格边界判、不做偏斜补偿。
     * 边界：exp == now 仍有效、exp = now-1s 即过期；iat == now 有效、iat = now+1s 即未生效。
     */
    @Test
    void AC_SDK_05_zeroClockSkewMeansStrictBoundaries() {
        GuanjinConfig zero = TestConfigs.minimal();
        zero.getGateway().setClockSkew(Duration.ZERO);

        ClaimValidator.validate(assertion(NOW, NOW, "guanjin-gateway", "orders"), zero, NOW);
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW.minusSeconds(1), NOW.minusSeconds(1),
                        "guanjin-gateway", "orders"), zero, NOW)).getCode());

        ClaimValidator.validate(assertion(NOW, NOW.plusSeconds(60), "guanjin-gateway", "orders"), zero, NOW);
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW.plusSeconds(1), NOW.plusSeconds(60),
                        "guanjin-gateway", "orders"), zero, NOW)).getCode());

        // 反证：默认 60s 下同样两条都不成立，证明上面是 skew=0 的效果
        GuanjinConfig dflt = TestConfigs.minimal(); // skew = 60s
        ClaimValidator.validate(assertion(NOW.minusSeconds(1), NOW.minusSeconds(1),
                "guanjin-gateway", "orders"), dflt, NOW);
        ClaimValidator.validate(assertion(NOW.plusSeconds(1), NOW.plusSeconds(60),
                "guanjin-gateway", "orders"), dflt, NOW);
    }

    @Test
    void AC_SDK_06_issAndAudMismatchFails() {
        GuanjinConfig cfg = TestConfigs.minimal();
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW, NOW.plusSeconds(60), "other-issuer", "orders"),
                        cfg, NOW)).getCode());
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW, NOW.plusSeconds(60), "guanjin-gateway", "payments"),
                        cfg, NOW)).getCode());
    }

    @Test
    void audFollowsClientIdByDefault() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getClient().setId("orders");
        assertEquals("orders", cfg.resolvedAud());
        // 显式配 aud 时以配置为准（偏离 client.id 需评审，但代码不拦——契约 #9 是"必须显式评审"）
        cfg.getGateway().setAud("orders-v2");
        assertEquals("orders-v2", cfg.resolvedAud());
        assertTrue(assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW, NOW.plusSeconds(60), "guanjin-gateway", "orders"),
                        cfg, NOW)).getMessage().contains("aud"));
    }

    @Test
    void missingExpOrIatFails() {
        GuanjinConfig cfg = TestConfigs.minimal();
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(null, NOW.plusSeconds(60), "guanjin-gateway", "orders"),
                        cfg, NOW)).getCode());
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW, null, "guanjin-gateway", "orders"),
                        cfg, NOW)).getCode());
    }

    @Test
    void skewIsConfigurable() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setClockSkew(Duration.ofSeconds(5));
        // exp 过 6s、skew 只有 5s → 拒
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> ClaimValidator.validate(assertion(NOW.minusSeconds(66), NOW.minusSeconds(6),
                        "guanjin-gateway", "orders"), cfg, NOW)).getCode());
    }
}
