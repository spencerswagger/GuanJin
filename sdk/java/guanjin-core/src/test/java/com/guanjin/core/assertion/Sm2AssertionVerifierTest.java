package com.guanjin.core.assertion;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.TestConfigs;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;
import com.guanjin.core.jwks.JwksClient;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.io.InputStream;
import java.net.URI;
import java.nio.charset.Charset;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * GJ-CAP-01 的**跨语言**证据：向量由平台自己的签发代码产出
 * （`go run ./artifacts/local/gen-sdk-vectors`，import `internal/signing`，即数据面签发路径），
 * 不是手搓的 SM2 签名，也不是自造 claim。
 *
 * <p>覆盖：合法断言验签通过、篡改 payload、未知 kid、`alg` 降级（用户令牌用途的 `SM2` 键）、
 * `exp` 过期、`iat` 在未来、`iss`/`aud` 不符。
 */
class Sm2AssertionVerifierTest {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static JsonNode fixture;
    private static String jwksJson;
    private static Instant referenceNow;

    @BeforeAll
    static void load() throws Exception {
        InputStream in = Sm2AssertionVerifierTest.class.getClassLoader()
                .getResourceAsStream("vectors/assertion-vectors.json");
        assertNotNull(in, "缺跨语言向量文件：sdk/java/guanjin-core/src/test/resources/vectors/assertion-vectors.json");
        byte[] buf = new byte[8192];
        StringBuilder sb = new StringBuilder();
        int n;
        while ((n = in.read(buf)) >= 0) {
            sb.append(new String(buf, 0, n, UTF8));
        }
        in.close();
        fixture = new ObjectMapper().readTree(sb.toString());
        jwksJson = fixture.get("jwks").toString();
        referenceNow = Instant.parse(fixture.get("referenceNow").asText());
    }

    @Test
    void validVectorFromRealSigningPathVerifies() {
        Assertion a = verifier().verify(vector("valid").get("token").asText());
        assertEquals("11111111-2222-4333-8444-555555555555", a.jti());
        assertEquals("guanjin-gateway", a.issuer());
        assertEquals("orders", a.audience());
        assertEquals("user-1", a.subject());
        assertEquals("alice", a.username());
        assertEquals(1, a.roles().size());
        assertEquals("user", a.roles().get(0));
        assertEquals("AAL2", a.acr());
        assertEquals("pwd", a.amr().get(0));
        assertEquals("orders", a.clientId());
        // claim 层：用向量生成时的"当前时刻"判定（向量是静态文件，不能拿运行时刻去比 exp）
        ClaimValidator.validate(a, cfg(), referenceNow);
    }

    @Test
    void tamperedPayloadIs401EvenThoughSignatureIsReal() {
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> verifier().verify(vector("tamperedPayload").get("token").asText()));
        assertEquals(401, e.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
        assertTrue(e.getMessage().contains("验签失败"), e.getMessage());
    }

    @Test
    void unknownKidIs401() {
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> verifier().verify(vector("unknownKid").get("token").asText()));
        assertEquals(401, e.getStatus());
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
    }

    @Test
    void oidcPurposeKeyCannotVerifyAssertion() {
        // alg=SM2（用户令牌用途）→ 拒绝算法降级，且不得拿用户令牌公钥验断言（GJ-CAP-02 密钥分离）
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> verifier().verify(vector("oidcAlg").get("token").asText()));
        assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode());
        assertTrue(e.getMessage().contains("alg"), e.getMessage());
    }

    @Test
    void expiredAndFutureIatAndIssAudMismatchAre401AtClaimLayer() {
        for (String name : new String[]{"expired", "futureIat", "wrongIss", "wrongAud"}) {
            Assertion a = verifier().verify(vector(name).get("token").asText());
            GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                    () -> ClaimValidator.validate(a, cfg(), referenceNow), name);
            assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode(), name);
            assertEquals(401, e.getStatus(), name);
        }
    }

    @Test
    void structurallyBrokenAssertionsAre401() {
        for (String bad : new String[]{"", "abc", "a.b", "a.b.c.d", "not-base64.not-base64.not-base64"}) {
            GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                    () -> verifier().verify(bad), "bad=" + bad);
            assertEquals(ErrorCodes.TOKEN_INVALID, e.getCode(), "bad=" + bad);
        }
    }

    private static JsonNode vector(String name) {
        for (JsonNode v : fixture.get("vectors")) {
            if (name.equals(v.get("name").asText())) {
                return v;
            }
        }
        throw new AssertionError("向量不存在：" + name);
    }

    private static GuanjinConfig cfg() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getClient().setId("orders");
        cfg.getGateway().setIssuer("guanjin-gateway");
        return cfg;
    }

    private static Sm2AssertionVerifier verifier() {
        final byte[] body = jwksJson.getBytes(UTF8);
        HttpFetcher fetcher = new HttpFetcher() {
            @Override
            public HttpResult get(URI uri, Map<String, String> headers) {
                return new HttpResult(200, body, "application/json");
            }
        };
        JwksClient jwks = new JwksClient(URI.create("http://127.0.0.1:18080/.well-known/jwks.json"),
                fetcher, Duration.ofMinutes(10), true);
        return new Sm2AssertionVerifier(jwks);
    }
}
