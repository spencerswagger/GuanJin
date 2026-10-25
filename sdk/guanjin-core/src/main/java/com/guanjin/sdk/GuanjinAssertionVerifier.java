package com.guanjin.sdk;

import com.guanjin.sdk.crypto.GuanjinSm2Verifier;
import com.guanjin.sdk.replay.GuanjinJtiReplayStore;
import com.nimbusds.jose.util.JSONObjectUtils;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import org.bouncycastle.math.ec.ECPoint;

public final class GuanjinAssertionVerifier {

    public static final String DEFAULT_ISSUER = "guanjin-gateway";
    public static final String ALG_SM3_WITH_SM2 = "SM3withSM2";
    public static final String ALG_SM2 = "SM2";

    private final GuanjinJwksCache jwksProvider;
    private final String expectedIssuer;
    private final String expectedAudience;
    private final long clockSkewSeconds;
    private final Clock clock;
    private final GuanjinJtiReplayStore replayStore;

    public GuanjinAssertionVerifier(GuanjinJwksCache jwksProvider, String expectedIssuer, String expectedAudience, long clockSkewSeconds) {
        this(jwksProvider, expectedIssuer, expectedAudience, clockSkewSeconds, Clock.systemUTC(), null);
    }

    public GuanjinAssertionVerifier(GuanjinJwksCache jwksProvider, String expectedIssuer, String expectedAudience,
                                    long clockSkewSeconds, GuanjinJtiReplayStore replayStore) {
        this(jwksProvider, expectedIssuer, expectedAudience, clockSkewSeconds, Clock.systemUTC(), replayStore);
    }

    public GuanjinAssertionVerifier(GuanjinJwksCache jwksProvider,
                             String expectedIssuer,
                             String expectedAudience,
                             long clockSkewSeconds,
                             Clock clock) {
        this(jwksProvider, expectedIssuer, expectedAudience, clockSkewSeconds, clock, null);
    }

    public GuanjinAssertionVerifier(GuanjinJwksCache jwksProvider,
                             String expectedIssuer,
                             String expectedAudience,
                             long clockSkewSeconds,
                             Clock clock,
                             GuanjinJtiReplayStore replayStore) {
        this.jwksProvider = jwksProvider;
        this.expectedIssuer = expectedIssuer == null ? DEFAULT_ISSUER : expectedIssuer;
        this.expectedAudience = expectedAudience;
        this.clockSkewSeconds = clockSkewSeconds;
        this.clock = clock == null ? Clock.systemUTC() : clock;
        this.replayStore = replayStore;
    }

    public GuanjinIdentity verify(String rawJwt) {
        if (rawJwt == null || rawJwt.isEmpty()) {
            throw new GuanjinAuthenticationException("内部令牌为空");
        }
        String[] parts = rawJwt.split("\\.");
        if (parts.length != 3) {
            throw new GuanjinAuthenticationException("内部令牌格式非法");
        }
        Map<String, Object> header = parseJson(parts[0], "令牌头");
        String alg = asString(header.get("alg"));
        if (alg != null && !ALG_SM3_WITH_SM2.equals(alg) && !ALG_SM2.equals(alg)) {
            throw new GuanjinAuthenticationException("不支持的令牌算法 alg=" + alg);
        }
        String kid = asString(header.get("kid"));
        ECPoint publicKey = jwksProvider.publicKey(kid);

        byte[] signingInput = (parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII);
        byte[] signature;
        try {
            signature = Base64.getUrlDecoder().decode(parts[2]);
        } catch (IllegalArgumentException e) {
            throw new GuanjinAuthenticationException("令牌签名编码非法");
        }
        if (!GuanjinSm2Verifier.verifySignature(publicKey, signingInput, signature)) {
            throw new GuanjinAuthenticationException("签名校验失败");
        }

        Map<String, Object> claims = parseJson(parts[1], "令牌负载");
        validateClaims(claims);
        GuanjinIdentity identity = toPrincipal(claims);
        if (replayStore != null) {
            replayStore.checkAndStore(identity.getJti(), identity.getExp());
        }
        return identity;
    }

    public void validateClaims(Map<String, Object> claims) {
        long now = clock.instant().getEpochSecond();
        String iss = asString(claims.get("iss"));
        if (expectedIssuer != null && !expectedIssuer.equals(iss)) {
            throw new GuanjinAuthenticationException("iss 不符");
        }
        if (expectedAudience != null) {
            if (!audienceMatches(claims.get("aud"))) {
                throw new GuanjinAuthenticationException("aud 不符");
            }
        }
        long exp = asLong(claims.get("exp"));
        if (exp <= 0) {
            throw new GuanjinAuthenticationException("令牌缺少 exp");
        }
        if (exp + clockSkewSeconds < now) {
            throw new GuanjinAuthenticationException("令牌已过期");
        }
        long iat = asLong(claims.get("iat"));
        if (iat > 0 && iat - clockSkewSeconds > now) {
            throw new GuanjinAuthenticationException("令牌尚未生效");
        }
        long nbf = asLong(claims.get("nbf"));
        if (nbf > 0 && nbf - clockSkewSeconds > now) {
            throw new GuanjinAuthenticationException("令牌尚未生效");
        }
    }

    public GuanjinIdentity toPrincipal(Map<String, Object> claims) {
        String sub = asString(claims.get("sub"));
        if (sub == null || sub.isEmpty()) {
            throw new GuanjinAuthenticationException("令牌缺少 sub");
        }
        String jti = asString(claims.get("jti"));
        if (jti == null || jti.isEmpty()) {
            throw new GuanjinAuthenticationException("令牌缺少 jti");
        }
        return new GuanjinIdentity(sub,
                asString(claims.get("aud")),
                asString(claims.get("cid")),
                asString(claims.get("uname")),
                asStringList(claims.get("roles")),
                asString(claims.get("acr")),
                asStringList(claims.get("amr")),
                asLong(claims.get("iat")),
                asLong(claims.get("exp")),
                asString(claims.get("jti")));
    }

    private boolean audienceMatches(Object aud) {
        if (aud == null) {
            return false;
        }
        if (aud instanceof List) {
            for (Object item : (List<?>) aud) {
                if (expectedAudience.equals(asString(item))) {
                    return true;
                }
            }
            return false;
        }
        return expectedAudience.equals(asString(aud));
    }

    private Map<String, Object> parseJson(String base64Url, String what) {
        try {
            byte[] decoded = Base64.getUrlDecoder().decode(base64Url);
            return JSONObjectUtils.parse(new String(decoded, StandardCharsets.UTF_8));
        } catch (Exception e) {
            throw new GuanjinAuthenticationException(what + "解析失败: " + e.getMessage(), e);
        }
    }

    private static String asString(Object value) {
        return value == null ? null : String.valueOf(value);
    }

    private static long asLong(Object value) {
        if (value instanceof Number) {
            return ((Number) value).longValue();
        }
        if (value == null) {
            return 0L;
        }
        try {
            return Long.parseLong(String.valueOf(value));
        } catch (NumberFormatException e) {
            return 0L;
        }
    }

    private static List<String> asStringList(Object value) {
        if (!(value instanceof List)) {
            return new ArrayList<>();
        }
        List<String> out = new ArrayList<>();
        for (Object item : (List<?>) value) {
            if (item != null) {
                out.add(String.valueOf(item));
            }
        }
        return out;
    }
}
