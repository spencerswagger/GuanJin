package com.guanjin.core.jwks;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.http.HttpFetchException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;

import java.io.IOException;
import java.net.URI;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * JWKS 客户端（契约 GJ-CAP-02/03/04）：启动拉一次并缓存，遇未知 {@code kid} 强刷一次。
 *
 * <p>fail-closed 口径（契约 §4.4）：
 * <ul>
 *   <li>拉取失败**且无缓存** → 503 {@code KEY_UNAVAILABLE}（不得降级放行，也不得用
 *       {@code DEPENDENCY_UNAVAILABLE}/{@code SERVICE_DEGRADED}）；</li>
 *   <li>拉取失败**但有缓存** → 沿用缓存继续**真实验签**（公钥公开，这不是降级）；</li>
 *   <li>刷过之后仍无该 {@code kid} → 401 {@code TOKEN_INVALID}（断言声称的键不在平台 JWKS 里）；</li>
 *   <li>{@code alg} 不是 {@code SM3withSM2} 的键**不参与**断言验签（与用户令牌密钥分离）。</li>
 * </ul>
 */
public final class JwksClient {

    private final URI jwksUri;
    private final HttpFetcher fetcher;
    private final Duration cacheTtl;
    private final boolean refreshOnUnknownKid;
    private final ObjectMapper mapper = new ObjectMapper();

    private volatile JwksSet cached;
    private volatile long cachedAtNanos;
    private volatile boolean everFetched;

    public JwksClient(URI jwksUri, HttpFetcher fetcher, Duration cacheTtl, boolean refreshOnUnknownKid) {
        if (jwksUri == null) {
            throw new IllegalArgumentException("jwksUri 不能为 null");
        }
        if (fetcher == null) {
            throw new IllegalArgumentException("fetcher 不能为 null");
        }
        this.jwksUri = jwksUri;
        this.fetcher = fetcher;
        this.cacheTtl = cacheTtl == null ? Duration.ofMinutes(10) : cacheTtl;
        this.refreshOnUnknownKid = refreshOnUnknownKid;
    }

    /** GJ-CAP-03：强制拉取一次并刷新缓存。失败抛 503 {@code KEY_UNAVAILABLE}。 */
    public synchronized JwksSet refresh() {
        HttpResult res;
        try {
            res = fetcher.get(jwksUri, Collections.<String, String>emptyMap());
        } catch (HttpFetchException e) {
            throw GuanjinSdkException.keyUnavailable(
                    "JWKS 不可达：" + jwksUri + "（连接/超时失败）", e);
        }
        if (!res.is2xx()) {
            throw GuanjinSdkException.keyUnavailable(
                    "JWKS 返回 " + res.status() + "：" + jwksUri
                            + (res.status() == 404
                            ? "（404 = 路径不存在：契约 C-2 冻结路径为 /.well-known/jwks.json，旧值 /api/v1/jwks 全库不存在）"
                            : ""),
                    null);
        }
        JwksSet parsed = parse(res.bodyAsString());
        this.cached = parsed;
        this.cachedAtNanos = System.nanoTime();
        this.everFetched = true;
        return parsed;
    }

    /** 当前缓存快照（可能为 {@code null}：从未成功拉取过）。 */
    public JwksSet cached() {
        return cached;
    }

    public boolean everFetched() {
        return everFetched;
    }

    public URI jwksUri() {
        return jwksUri;
    }

    /** 缓存是否过期（TTL = 配置 #13）。 */
    public boolean cacheExpired() {
        JwksSet snap = cached;
        if (snap == null) {
            return true;
        }
        long ageNanos = System.nanoTime() - cachedAtNanos;
        return ageNanos > cacheTtl.toNanos();
    }

    /**
     * GJ-CAP-02/04：按 {@code kid} 取网关用途公钥。
     *
     * @throws GuanjinSdkException 503 {@code KEY_UNAVAILABLE}（无缓存且拉不到）或
     *                             401 {@code TOKEN_INVALID}（键不存在）
     */
    public JwksKey publicKey(String kid) {
        if (kid == null || kid.trim().isEmpty()) {
            throw GuanjinSdkException.tokenInvalid("断言 header 里没有 kid（无法选键）");
        }
        JwksSet snap = cached;
        if (snap == null || cacheExpired()) {
            try {
                snap = refresh();
            } catch (GuanjinSdkException e) {
                if (cached == null) {
                    throw e; // 无缓存 → 503 KEY_UNAVAILABLE
                }
                snap = cached; // 有缓存 → 沿用（非降级，验签仍真实执行）
            }
        }
        JwksKey key = snap.gatewayKey(kid);
        if (key != null) {
            return key;
        }
        if (!refreshOnUnknownKid) {
            throw GuanjinSdkException.tokenInvalid("kid=" + kid + " 不在 JWKS 缓存里（未开启未知 kid 强刷）");
        }
        // 未知 kid → 强刷一次（应对密钥轮换）
        JwksSet refreshed;
        try {
            refreshed = refresh();
        } catch (GuanjinSdkException e) {
            throw GuanjinSdkException.tokenInvalid(
                    "kid=" + kid + " 未知且强刷失败（仍拒绝，不放行）", e);
        }
        JwksKey after = refreshed.gatewayKey(kid);
        if (after == null) {
            throw GuanjinSdkException.tokenInvalid(
                    "kid=" + kid + " 强刷后仍不存在于 JWKS（现有网关键=" + refreshed.gatewayKeys().size() + " 把）");
        }
        return after;
    }

    private JwksSet parse(String body) {
        JsonNode root;
        try {
            root = mapper.readTree(body);
        } catch (IOException e) {
            throw GuanjinSdkException.keyUnavailable("JWKS 不是合法 JSON：" + jwksUri, e);
        }
        JsonNode keysNode = root == null ? null : root.get("keys");
        if (keysNode == null || !keysNode.isArray()) {
            throw GuanjinSdkException.keyUnavailable("JWKS 缺少 keys 数组：" + jwksUri, null);
        }
        List<JwksKey> keys = new ArrayList<JwksKey>(keysNode.size());
        for (JsonNode n : keysNode) {
            String kid = text(n, "kid");
            if (kid == null || kid.isEmpty()) {
                continue; // 无 kid 的键无法选键，忽略（不算拉取失败）
            }
            keys.add(new JwksKey(kid, text(n, "kty"), text(n, "crv"), text(n, "use"),
                    text(n, "alg"), text(n, "x"), text(n, "y")));
        }
        if (keys.isEmpty()) {
            throw GuanjinSdkException.keyUnavailable("JWKS 里没有任何可用 kid：" + jwksUri, null);
        }
        return new JwksSet(keys);
    }

    private static String text(JsonNode n, String field) {
        JsonNode v = n.get(field);
        return v == null || v.isNull() ? null : v.asText();
    }
}
