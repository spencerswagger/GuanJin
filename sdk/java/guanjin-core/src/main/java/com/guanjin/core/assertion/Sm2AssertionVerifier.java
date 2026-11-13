package com.guanjin.core.assertion;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.crypto.Sm2;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.jwks.JwksClient;
import com.guanjin.core.jwks.JwksKey;

import java.io.IOException;
import java.nio.charset.Charset;
import java.security.PublicKey;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;

/**
 * 断言验签的生产实现（契约 GJ-CAP-01 {@code AssertionVerifier.verify(raw)}）：
 * **SM2 真验签**（{@code alg=SM2}，D29：平台 keygen 写死）+ {@code kid} 选键 + {@code alg} 不协商。
 *
 * <p>失败一律 401 {@code TOKEN_INVALID}（契约 §4.4：不区分"过期"与"伪造"）：
 * 结构坏、{@code alg} 不是 {@code SM2}、缺 {@code kid}、未知 {@code kid}、
 * 公钥不在曲线上、签名不匹配 —— 全部拒，**没有放行分支**。
 *
 * <p>取公钥失败（JWKS 不可得且无缓存）仍按 503 {@code KEY_UNAVAILABLE} 抛，
 * 由 {@link JwksClient} 决定；验签失败与密钥不可得是两件事，不混。
 */
public final class Sm2AssertionVerifier implements AssertionVerifier {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final Base64.Decoder B64URL = Base64.getUrlDecoder();

    private final JwksClient jwks;
    private final ObjectMapper json = new ObjectMapper();

    public Sm2AssertionVerifier(JwksClient jwks) {
        if (jwks == null) {
            throw new IllegalArgumentException("JwksClient 不能为 null（GJ-CAP-02/03 取公钥的唯一来源）");
        }
        this.jwks = jwks;
    }

    @Override
    public Assertion verify(String rawAssertion) {
        if (rawAssertion == null || rawAssertion.trim().isEmpty()) {
            throw GuanjinSdkException.tokenInvalid("断言为空");
        }
        String raw = rawAssertion.trim();
        String[] parts = raw.split("\\.", -1);
        if (parts.length != 3) {
            throw GuanjinSdkException.tokenInvalid("断言结构坏：不是三段 JWS（header.payload.signature）");
        }
        JsonNode header = readJson(parts[0], "header");
        String alg = text(header, "alg");
        if (!Sm2.ALG.equals(alg)) {
            // 防算法降级：只认 SM2（D29），不协商、不猜
            throw GuanjinSdkException.tokenInvalid("断言 alg 不是 " + Sm2.ALG + "（得到 " + alg + "），拒绝算法降级");
        }
        String kid = text(header, "kid");
        if (kid == null || kid.isEmpty()) {
            throw GuanjinSdkException.tokenInvalid("断言 header 缺 kid（无法选键）");
        }

        // GJ-CAP-02/04：按 kid 取网关用途公钥；未知 kid 会强刷一次，仍无则 401
        JwksKey key = jwks.publicKey(kid);
        PublicKey publicKey;
        try {
            publicKey = Sm2.publicKey(decode(key.x()), decode(key.y()));
        } catch (RuntimeException e) {
            throw GuanjinSdkException.tokenInvalid("JWKS 里的公钥不可用（kid=" + kid + "）", e);
        }

        byte[] signingInput = (parts[0] + "." + parts[1]).getBytes(UTF8);
        byte[] signature = decodeSignature(parts[2]);
        if (!Sm2.verify(publicKey, signingInput, signature)) {
            throw GuanjinSdkException.tokenInvalid("断言验签失败（SM2 不匹配）");
        }

        JsonNode claims = readJson(parts[1], "payload");
        return toAssertion(claims);
    }

    private Assertion toAssertion(JsonNode claims) {
        String jti = text(claims, "jti");
        if (jti == null || jti.trim().isEmpty()) {
            // 没有 jti = 防重放没有对象（AC-DP-08），拒
            throw GuanjinSdkException.tokenInvalid("断言缺 jti（无法做防重放）");
        }
        Instant exp = instant(claims, "exp");
        Instant iat = instant(claims, "iat");
        return new Assertion(jti, text(claims, "iss"), text(claims, "aud"), exp, iat,
                text(claims, "sub"), text(claims, "uname"), strings(claims, "roles"),
                text(claims, "acr"), strings(claims, "amr"), text(claims, "cid"));
    }

    private JsonNode readJson(String part, String what) {
        try {
            JsonNode node = json.readTree(new String(B64URL.decode(part), UTF8));
            if (node == null || !node.isObject()) {
                throw GuanjinSdkException.tokenInvalid("断言 " + what + " 不是 JSON 对象");
            }
            return node;
        } catch (IllegalArgumentException e) {
            throw GuanjinSdkException.tokenInvalid("断言 " + what + " 不是合法 base64url", e);
        } catch (IOException e) {
            throw GuanjinSdkException.tokenInvalid("断言 " + what + " 不是合法 JSON", e);
        }
    }

    private static byte[] decodeSignature(String part) {
        byte[] sig;
        try {
            sig = B64URL.decode(part);
        } catch (IllegalArgumentException e) {
            throw GuanjinSdkException.tokenInvalid("断言签名不是合法 base64url", e);
        }
        if (sig.length != 64) {
            throw GuanjinSdkException.tokenInvalid("断言签名长度必须是 64 字节 r||s（得到 " + sig.length + "）");
        }
        return sig;
    }

    private static byte[] decode(String base64Url) {
        if (base64Url == null) {
            throw new IllegalArgumentException("缺坐标");
        }
        return B64URL.decode(base64Url);
    }

    private static String text(JsonNode node, String field) {
        JsonNode v = node.get(field);
        return v == null || v.isNull() ? null : v.asText();
    }

    private static Instant instant(JsonNode claims, String field) {
        JsonNode v = claims.get(field);
        if (v == null || v.isNull()) {
            return null;
        }
        long epochSeconds;
        if (v.isNumber()) {
            epochSeconds = v.asLong();
        } else {
            try {
                epochSeconds = Long.parseLong(v.asText());
            } catch (NumberFormatException e) {
                throw GuanjinSdkException.tokenInvalid("断言 " + field + " 不是 epoch 秒", e);
            }
        }
        return Instant.ofEpochSecond(epochSeconds);
    }

    private static List<String> strings(JsonNode claims, String field) {
        JsonNode v = claims.get(field);
        List<String> out = new ArrayList<String>();
        if (v == null || v.isNull()) {
            return out;
        }
        if (!v.isArray()) {
            throw GuanjinSdkException.tokenInvalid("断言 " + field + " 必须是数组");
        }
        for (JsonNode n : v) {
            out.add(n.asText());
        }
        return out;
    }
}
