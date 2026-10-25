package com.guanjin.sdk;

import com.guanjin.sdk.crypto.GuanjinSm2Verifier;
import com.nimbusds.jose.util.JSONObjectUtils;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.bouncycastle.math.ec.ECPoint;

public final class GuanjinJwksCache {

    public static final String ALG_GATEWAY = "SM2";
    public static final String ALG_USER_TOKEN = "SM3withSM2";

    @FunctionalInterface
    public interface Fetcher {
        String fetch(String uri) throws Exception;
    }

    private final String jwksUri;
    private final Duration cacheTtl;
    private final Fetcher fetcher;
    private final Clock clock;
    private final Map<String, CachedKey> cache = new LinkedHashMap<>();
    private volatile Instant loadedAt = Instant.EPOCH;

    public GuanjinJwksCache(String jwksUri, Duration cacheTtl) {
        this(jwksUri, cacheTtl, GuanjinJwksCache::fetchHttp, Clock.systemUTC());
    }

    public GuanjinJwksCache(String jwksUri, Duration cacheTtl, Duration readTimeout) {
        this(jwksUri, cacheTtl, uri -> fetchHttp(uri, readTimeout), Clock.systemUTC());
    }

    public GuanjinJwksCache(String jwksUri, Duration cacheTtl, Fetcher fetcher) {
        this(jwksUri, cacheTtl, fetcher, Clock.systemUTC());
    }

    private static String fetchHttp(String uri) throws Exception {
        return fetchHttp(uri, Duration.ofSeconds(3));
    }

    private static String fetchHttp(String uri, Duration timeout) throws Exception {
        java.net.http.HttpClient client = java.net.http.HttpClient.newBuilder()
                .connectTimeout(timeout == null ? Duration.ofSeconds(2) : timeout)
                .build();
        java.net.http.HttpRequest request = java.net.http.HttpRequest.newBuilder(java.net.URI.create(uri))
                .timeout(timeout == null ? Duration.ofSeconds(3) : timeout)
                .GET()
                .build();
        java.net.http.HttpResponse<String> response = client.send(request,
                java.net.http.HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
        if (response.statusCode() / 100 != 2) {
            throw new IllegalStateException("JWKS HTTP " + response.statusCode());
        }
        return response.body();
    }

    public GuanjinJwksCache(String jwksUri, Duration cacheTtl, Fetcher fetcher, Clock clock) {
        this.jwksUri = jwksUri;
        this.cacheTtl = cacheTtl == null ? Duration.ofMinutes(10) : cacheTtl;
        this.fetcher = fetcher;
        this.clock = clock == null ? Clock.systemUTC() : clock;
    }

    public ECPoint publicKey(String kid) {
        CachedKey cached = lookup(kid);
        if (cached != null && !stale()) {
            return cached.point;
        }
        refreshQuietly();
        cached = lookup(kid);
        if (cached != null) {
            return cached.point;
        }
        throw new GuanjinAuthenticationException("未找到可用验签公钥 kid=" + kid);
    }

    public synchronized void forceRefresh() {
        refreshQuietly();
    }

    public int cachedKeyCount() {
        synchronized (this) {
            return cache.size();
        }
    }

    private CachedKey lookup(String kid) {
        synchronized (this) {
            CachedKey byKid = cache.get(kid);
            if (byKid != null) {
                return byKid;
            }
            if (kid == null || kid.isEmpty()) {
                return cache.get(GATEWAY_FALLBACK);
            }
            return null;
        }
    }

    private static final String GATEWAY_FALLBACK = "\u0000gateway";

    private boolean stale() {
        return clock.instant().isAfter(loadedAt.plus(cacheTtl));
    }

    private void refreshQuietly() {
        try {
            String json = fetcher.fetch(jwksUri);
            Map<String, CachedKey> parsed = parse(json);
            synchronized (this) {
                cache.clear();
                cache.putAll(parsed);
                if (!cache.containsKey(GATEWAY_FALLBACK)) {
                    CachedKey gateway = firstByAlg(parsed, ALG_GATEWAY);
                    if (gateway != null) {
                        cache.put(GATEWAY_FALLBACK, gateway);
                    }
                }
                loadedAt = clock.instant();
            }
        } catch (Exception e) {
            boolean empty;
            synchronized (this) {
                empty = cache.isEmpty();
            }
            if (empty) {
                throw new GuanjinDependencyException("JWKS 不可用且无本地缓存: " + e.getMessage(), e);
            }
        }
    }

    private static CachedKey firstByAlg(Map<String, CachedKey> keys, String alg) {
        for (CachedKey key : keys.values()) {
            if (alg.equals(key.alg)) {
                return key;
            }
        }
        return null;
    }

    private static Map<String, CachedKey> parse(String json) throws Exception {
        Map<String, Object> document = JSONObjectUtils.parse(json);
        Object keys = document.get("keys");
        if (!(keys instanceof List)) {
            throw new GuanjinAuthenticationException("JWKS 缺少 keys 数组");
        }
        Map<String, CachedKey> result = new LinkedHashMap<>();
        for (Object item : (List<?>) keys) {
            if (!(item instanceof Map)) {
                continue;
            }
            @SuppressWarnings("unchecked")
            Map<String, Object> jwk = (Map<String, Object>) item;
            String kty = asString(jwk.get("kty"));
            if (kty != null && !"EC".equals(kty)) {
                continue;
            }
            String x = asString(jwk.get("x"));
            String y = asString(jwk.get("y"));
            if (x == null || y == null) {
                continue;
            }
            ECPoint point = GuanjinSm2Verifier.publicKey(Base64.getUrlDecoder().decode(x), Base64.getUrlDecoder().decode(y));
            CachedKey key = new CachedKey(asString(jwk.get("kid")), asString(jwk.get("alg")), point);
            if (key.kid != null) {
                result.put(key.kid, key);
            }
        }
        if (result.isEmpty()) {
            throw new GuanjinAuthenticationException("JWKS 中无可用的 SM2 公钥");
        }
        return result;
    }

    private static String asString(Object value) {
        return value == null ? null : String.valueOf(value);
    }

    static byte[] ascii(String value) {
        return value.getBytes(StandardCharsets.US_ASCII);
    }

    private static final class CachedKey {
        private final String kid;
        private final String alg;
        private final ECPoint point;

        private CachedKey(String kid, String alg, ECPoint point) {
            this.kid = kid;
            this.alg = alg;
            this.point = point;
        }
    }
}
