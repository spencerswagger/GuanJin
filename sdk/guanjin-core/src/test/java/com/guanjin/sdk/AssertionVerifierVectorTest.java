package com.guanjin.sdk;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.guanjin.sdk.crypto.Sm2;
import com.guanjin.sdk.crypto.Sm3;
import com.nimbusds.jose.util.JSONObjectUtils;
import java.math.BigInteger;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class AssertionVerifierVectorTest {

    private Map<String, Object> vector;
    private JwksProvider jwksProvider;
    private AssertionVerifier verifier;

    @BeforeEach
    @SuppressWarnings("unchecked")
    void setUp() throws Exception {
        String json = new String(getClass().getResourceAsStream("/jwt_vector.json").readAllBytes(),
                StandardCharsets.UTF_8);
        vector = JSONObjectUtils.parse(json);
        String jwksJson = JSONObjectUtils.toJSONString((Map<String, Object>) vector.get("jwks"));
        jwksProvider = new JwksProvider("https://platform.example.com/.well-known/jwks.json",
                Duration.ofMinutes(10), uri -> jwksJson);
        verifier = new AssertionVerifier(jwksProvider, "guanjin-gateway", "order-service", 60);
    }

    @Test
    void verifiesGoSignedToken_cross_language() {
        String token = (String) vector.get("token");
        GuanjinPrincipal principal = verifier.verify(token);

        assertThat(principal.getSub()).isEqualTo("u_12345");
        assertThat(principal.getAud()).isEqualTo("order-service");
        assertThat(principal.getCid()).isEqualTo("order-portal");
        assertThat(principal.getUname()).isEqualTo("zhangsan");
        assertThat(principal.getRoles()).containsExactly("order_admin", "auditor");
        assertThat(principal.getAcr()).isEqualTo("AAL2");
        assertThat(principal.getAmr()).containsExactly("pwd", "otp");
        assertThat(principal.getExp()).isEqualTo(4102444800L);
        assertThat(principal.getJti()).isEqualTo("2f8c1b90-2a3d-4e5f-8091-a2b3c4d5e6f7");
    }

    @Test
    void verifiesJavaSignedToken_round_trip_against_go_jwks() {
        BigInteger d = new BigInteger(1, Base64.getUrlDecoder().decode((String) vector.get("private_key_d")));
        String token = signToken(d, "u_77777", "order-service",
                "{\"iss\":\"guanjin-gateway\",\"sub\":\"u_77777\",\"aud\":\"order-service\",\"cid\":\"c\","
                        + "\"uname\":\"lisi\",\"roles\":[\"order_viewer\"],\"acr\":\"AAL1\",\"amr\":[],"
                        + "\"iat\":1730000000,\"exp\":4102444800,\"jti\":\"java-jti-1\"}");

        GuanjinPrincipal principal = verifier.verify(token);
        assertThat(principal.getSub()).isEqualTo("u_77777");
        assertThat(principal.getRoles()).containsExactly("order_viewer");
    }

    @Test
    void rejectsTamperedSignature() {
        String token = (String) vector.get("token");
        String[] parts = token.split("\\.");
        char last = parts[2].charAt(parts[2].length() - 1);
        char swapped = last == 'A' ? 'B' : 'A';
        String tampered = parts[0] + "." + parts[1] + "."
                + parts[2].substring(0, parts[2].length() - 1) + swapped;
        assertThatThrownBy(() -> verifier.verify(tampered))
                .isInstanceOf(GuanjinAuthenticationException.class);
    }

    @Test
    void rejectsWrongIssuer() {
        AssertionVerifier other = new AssertionVerifier(jwksProvider, "other-issuer", "order-service", 60);
        assertThatThrownBy(() -> other.verify((String) vector.get("token")))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("iss");
    }

    @Test
    void rejectsWrongAudience() {
        AssertionVerifier other = new AssertionVerifier(jwksProvider, "guanjin-gateway", "other-service", 60);
        assertThatThrownBy(() -> other.verify((String) vector.get("token")))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("aud");
    }

    @Test
    void rejectsExpiredToken() {
        BigInteger d = new BigInteger(1, Base64.getUrlDecoder().decode((String) vector.get("private_key_d")));
        long past = System.currentTimeMillis() / 1000L - 1000;
        String token = signToken(d, "u_1", "order-service",
                "{\"iss\":\"guanjin-gateway\",\"sub\":\"u_1\",\"aud\":\"order-service\",\"roles\":[],"
                        + "\"exp\":" + past + ",\"iat\":" + (past - 60) + ",\"jti\":\"expired-1\"}");
        assertThatThrownBy(() -> verifier.verify(token))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("过期");
    }

    @Test
    void rejectsUnsupportedAlgorithm() {
        BigInteger d = new BigInteger(1, Base64.getUrlDecoder().decode((String) vector.get("private_key_d")));
        Map<String, Object> header = new LinkedHashMap<>();
        header.put("alg", "RS256");
        header.put("kid", "gw:testkey-0001");
        header.put("typ", "JWT");
        String token = signTokenWithHeader(d, header, "{\"iss\":\"guanjin-gateway\",\"sub\":\"u_1\","
                + "\"aud\":\"order-service\",\"exp\":4102444800,\"jti\":\"bad-alg\"}");
        assertThatThrownBy(() -> verifier.verify(token))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("算法");
    }

    private String signToken(BigInteger d, String sub, String aud, String payloadJson) {
        Map<String, Object> header = new LinkedHashMap<>();
        header.put("alg", "SM3withSM2");
        header.put("kid", "gw:testkey-0001");
        header.put("typ", "JWT");
        return signTokenWithHeader(d, header, payloadJson);
    }

    private String signTokenWithHeader(BigInteger d, Map<String, Object> header, String payloadJson) {
        String headerB64 = base64Url(JSONObjectUtils.toJSONString(header).getBytes(StandardCharsets.UTF_8));
        String payloadB64 = base64Url(payloadJson.getBytes(StandardCharsets.UTF_8));
        byte[] signingInput = (headerB64 + "." + payloadB64).getBytes(StandardCharsets.US_ASCII);
        byte[] digest = Sm3.digest(signingInput);
        byte[] signature = Sm2.sign(d, digest);
        return headerB64 + "." + payloadB64 + "." + base64Url(signature);
    }

    private static String base64Url(byte[] data) {
        return Base64.getUrlEncoder().withoutPadding().encodeToString(data);
    }
}
