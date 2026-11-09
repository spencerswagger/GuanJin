package com.guanjin.core.jwks;

/**
 * JWKS 里的一把公钥（原始坐标，未构造 {@code PublicKey}）。
 *
 * <p>平台 JWKS 的形态（{@code internal/signing/signing.go:194}）：
 * {@code {"kty":"EC","crv":"SM2","kid":"…","use":"sig","alg":"SM3withSM2|SM2","x":"…","y":"…"}}，
 * {@code x}/{@code y} 是 32 字节定长坐标的 base64url。
 *
 * <p>为什么这里只留原始坐标、不直接造 {@code PublicKey}：SM2 曲线不在 JDK 8 的 JCA 里
 * （也不在 nimbus 支持的曲线集里），公钥构造属于验签切片（BouncyCastle）的职责。
 * 本切片只做"取到了哪几把键、kid 是哪几个"的**可验证**事实。
 */
public final class JwksKey {

    /** 断言验签唯一允许的算法（契约 §4.1 M2：`SM3withSM2`，不做协商降级）。 */
    public static final String ALG_SM3_WITH_SM2 = "SM3withSM2";

    /** 对外令牌（用户令牌）用的算法：**不属于** SDK 的验签面（GJ-CAP-02：与用户令牌密钥分离）。 */
    public static final String ALG_SM2 = "SM2";

    private final String kid;
    private final String kty;
    private final String crv;
    private final String use;
    private final String alg;
    private final String x;
    private final String y;

    public JwksKey(String kid, String kty, String crv, String use, String alg, String x, String y) {
        this.kid = kid;
        this.kty = kty;
        this.crv = crv;
        this.use = use;
        this.alg = alg;
        this.x = x;
        this.y = y;
    }

    public String kid() {
        return kid;
    }

    public String kty() {
        return kty;
    }

    public String crv() {
        return crv;
    }

    public String use() {
        return use;
    }

    public String alg() {
        return alg;
    }

    public String x() {
        return x;
    }

    public String y() {
        return y;
    }

    /**
     * 是否是**网关用途**的验签公钥（{@code alg=SM3withSM2}）。
     *
     * <p>JWKS 里同时有 {@code oidc}（{@code alg=SM2}，签用户令牌）与 {@code gateway}
     * （{@code alg=SM3withSM2}，签内部断言）两类键，靠 {@code kid} 区分。SDK 只验内部断言，
     * 因此只允许用 gateway 用途的键——**不得**拿用户令牌公钥来验断言。
     */
    public boolean isGatewayKey() {
        return ALG_SM3_WITH_SM2.equals(alg);
    }

    @Override
    public String toString() {
        return "JwksKey{kid=" + kid + ", kty=" + kty + ", crv=" + crv + ", alg=" + alg + "}";
    }
}
