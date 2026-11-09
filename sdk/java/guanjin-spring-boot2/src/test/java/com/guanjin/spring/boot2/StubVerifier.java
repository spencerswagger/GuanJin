package com.guanjin.spring.boot2;

import com.guanjin.core.assertion.Assertion;
import com.guanjin.core.assertion.AssertionVerifier;
import com.guanjin.core.error.GuanjinSdkException;

import java.time.Instant;
import java.util.Arrays;

/**
 * **测试专用**验签器：SM2 验签属于后续切片，本切片只验证"验签之后"的链路
 * （claim → jti 重放 → 剥离 → 上下文），故用可预测的令牌语法驱动。
 *
 * <p>令牌语法：{@code stub:<jti>}（其余一律 401），并可选 {@code stub:<jti>:badiss} / {@code :badaud}
 * 制造 claim 不符。**它不是生产实现**：生产环境没有 {@code AssertionVerifier} bean 时启动会失败。
 */
public final class StubVerifier implements AssertionVerifier {

    private final String issuer;
    private final String audience;

    public StubVerifier(String issuer, String audience) {
        this.issuer = issuer;
        this.audience = audience;
    }

    @Override
    public Assertion verify(String rawAssertion) {
        if (rawAssertion == null || !rawAssertion.startsWith("stub:")) {
            throw GuanjinSdkException.tokenInvalid("验签失败（stub 只接受 stub:<jti>）");
        }
        String[] parts = rawAssertion.split(":");
        String jti = parts.length > 1 ? parts[1] : "";
        String iss = issuer;
        String aud = audience;
        if (parts.length > 2 && "badiss".equals(parts[2])) {
            iss = "other-issuer";
        }
        if (parts.length > 2 && "badaud".equals(parts[2])) {
            aud = "payments";
        }
        Instant now = Instant.now();
        return new Assertion(jti, iss, aud, now.plusSeconds(60), now, "user-1", "alice",
                Arrays.asList("user"), "AAL2", Arrays.asList("pwd"), audience);
    }
}
