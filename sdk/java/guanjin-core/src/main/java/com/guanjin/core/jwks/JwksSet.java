package com.guanjin.core.jwks;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/** 一份 JWKS 快照（不可变）。 */
public final class JwksSet {

    private final List<JwksKey> keys;

    public JwksSet(List<JwksKey> keys) {
        this.keys = Collections.unmodifiableList(new ArrayList<JwksKey>(keys));
    }

    public List<JwksKey> keys() {
        return keys;
    }

    /** 全部 kid（顺序同 JWKS；用于启动自检打印与排障）。 */
    public List<String> kids() {
        List<String> out = new ArrayList<String>(keys.size());
        for (JwksKey k : keys) {
            out.add(k.kid());
        }
        return out;
    }

    /** 只返回网关用途的键（{@code alg=SM2}，D29 双轨），SDK 验签只用这些。 */
    public List<JwksKey> gatewayKeys() {
        List<JwksKey> out = new ArrayList<JwksKey>(keys.size());
        for (JwksKey k : keys) {
            if (k.isGatewayKey()) {
                out.add(k);
            }
        }
        return out;
    }

    /** 按 kid 找网关用途的键；找不到返回 {@code null}（调用方决定 401/503 语义）。 */
    public JwksKey gatewayKey(String kid) {
        if (kid == null) {
            return null;
        }
        for (JwksKey k : keys) {
            if (kid.equals(k.kid()) && k.isGatewayKey()) {
                return k;
            }
        }
        return null;
    }

    public int size() {
        return keys.size();
    }
}
