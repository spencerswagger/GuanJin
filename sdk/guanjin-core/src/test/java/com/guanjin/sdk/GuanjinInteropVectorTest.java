package com.guanjin.sdk;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.guanjin.sdk.crypto.GuanjinSm2Verifier;
import com.guanjin.sdk.crypto.GuanjinSm3;
import com.guanjin.sdk.replay.GuanjinInMemoryJtiReplayStore;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.Map;
import org.bouncycastle.math.ec.ECPoint;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class GuanjinInteropVectorTest {

    private Map<String, Object> meta;
    private GuanjinJwksCache jwksCache;
    private GuanjinAssertionVerifier verifier;

    @BeforeEach
    void setUp() {
        meta = TestVectors.meta();
        String jwksJson = TestVectors.read("jwks.json");
        jwksCache = new GuanjinJwksCache("https://platform.example.com/api/v1/jwks",
                Duration.ofMinutes(10), uri -> jwksJson);
        verifier = new GuanjinAssertionVerifier(jwksCache,
                String.valueOf(meta.get("issuer")), String.valueOf(meta.get("audience")), 60);
    }

    @Test
    void verifiesGoGeneratedAssertion() {
        String token = TestVectors.read("assertion.jwt").trim();
        GuanjinIdentity identity = verifier.verify(token);

        assertThat(identity.getSub()).isEqualTo("u_12345");
        assertThat(identity.getUname()).isEqualTo("zhangsan");
        assertThat(identity.getCid()).isEqualTo("order-portal");
        assertThat(identity.getAud()).isEqualTo("order-service");
        assertThat(identity.getRoles()).containsExactly("ADMIN");
        assertThat(identity.getAcr()).isEqualTo("AAL2");
        assertThat(identity.getAmr()).containsExactly("pwd", "otp");
        assertThat(identity.getExp()).isEqualTo(4102444800L);
        assertThat(identity.getJti()).isEqualTo("2f8c1b90-2a3d-4e5f-8091-a2b3c4d5e6f7");

        String[] parts = token.split("\\.");
        assertThat(parts.length).isEqualTo(3);
        assertThat(meta.get("signing_input")).isEqualTo(parts[0] + "." + parts[1]);
    }

    @Test
    void lowLevelSm2VerifierMatchesGoRawDigestSignature() {
        String token = TestVectors.read("assertion.jwt").trim();
        String[] parts = token.split("\\.");
        ECPoint publicKey = jwksCache.publicKey(String.valueOf(meta.get("kid")));
        byte[] signingInput = (parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII);
        byte[] signature = Base64.getUrlDecoder().decode(parts[2]);

        assertThat(GuanjinSm2Verifier.verifySignature(publicKey, signingInput, signature)).isTrue();
        assertThat(GuanjinSm2Verifier.verifyDigest(publicKey, GuanjinSm3.digest(signingInput), signature)).isTrue();

        byte[] wrongDigest = GuanjinSm3.digest("tampered".getBytes(StandardCharsets.US_ASCII));
        assertThat(GuanjinSm2Verifier.verifyDigest(publicKey, wrongDigest, signature)).isFalse();
    }

    @Test
    void rejectsTamperedPayload() {
        String token = TestVectors.read("assertion.jwt").trim();
        String[] parts = token.split("\\.");
        String payload = new String(Base64.getUrlDecoder().decode(parts[1]), StandardCharsets.UTF_8);
        String tamperedPayload = payload.replace("\"uname\":\"zhangsan\"", "\"uname\":\"mallory\"");
        assertThat(tamperedPayload).isNotEqualTo(payload);
        String tampered = parts[0] + "."
                + Base64.getUrlEncoder().withoutPadding()
                        .encodeToString(tamperedPayload.getBytes(StandardCharsets.UTF_8))
                + "." + parts[2];

        assertThatThrownBy(() -> verifier.verify(tampered))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("签名");
    }

    @Test
    void rejectsTamperedSignature() {
        String token = TestVectors.read("assertion.jwt").trim();
        String[] parts = token.split("\\.");
        char last = parts[2].charAt(parts[2].length() - 1);
        char swapped = last == 'A' ? 'B' : 'A';
        String tampered = parts[0] + "." + parts[1] + "."
                + parts[2].substring(0, parts[2].length() - 1) + swapped;

        assertThatThrownBy(() -> verifier.verify(tampered))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("签名");
    }

    @Test
    void rejectsWrongIssuer() {
        assertThatThrownBy(() -> verifier.verify(TestVectors.read("assertion_wrongiss.jwt").trim()))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("iss");
    }

    @Test
    void rejectsWrongAudience() {
        assertThatThrownBy(() -> verifier.verify(TestVectors.read("assertion_wrongaud.jwt").trim()))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("aud");
    }

    @Test
    void rejectsExpiredAssertion() {
        assertThatThrownBy(() -> verifier.verify(TestVectors.read("assertion_expired.jwt").trim()))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("过期");
    }

    @Test
    void rejectsReplayedJti() {
        GuanjinInMemoryJtiReplayStore store = new GuanjinInMemoryJtiReplayStore();
        GuanjinAssertionVerifier withReplay = new GuanjinAssertionVerifier(jwksCache,
                String.valueOf(meta.get("issuer")), String.valueOf(meta.get("audience")), 60, store);
        String token = TestVectors.read("assertion.jwt").trim();

        assertThat(withReplay.verify(token).getSub()).isEqualTo("u_12345");
        assertThatThrownBy(() -> withReplay.verify(token))
                .isInstanceOf(GuanjinReplayException.class);
    }
}
