package com.guanjin.core.crypto;

import org.bouncycastle.asn1.ASN1Encodable;
import org.bouncycastle.asn1.ASN1Encoding;
import org.bouncycastle.asn1.ASN1Integer;
import org.bouncycastle.asn1.DERSequence;
import org.bouncycastle.asn1.gm.GMNamedCurves;
import org.bouncycastle.asn1.x9.X9ECParameters;
import org.bouncycastle.jce.provider.BouncyCastleProvider;
import org.bouncycastle.jce.spec.ECParameterSpec;
import org.bouncycastle.jce.spec.ECPublicKeySpec;

import java.math.BigInteger;
import java.security.KeyFactory;
import java.security.PublicKey;
import java.security.Security;
import java.security.Signature;
import java.util.Arrays;

/**
 * 国密 SM2 的**最小**工具面（GJ-CAP-01 的密码学底座）：
 * 公钥从 JWKS 的 {@code x}/{@code y} 构造、{@code r||s} 与 DER 互转、{@code SM3withSM2} 验签。
 *
 * <p>三条与平台（Go 侧 {@code internal/signing}）必须逐字节对齐的口径：
 * <ul>
 *   <li>曲线 {@code sm2p256v1}（GM/T 0003）；{@code x}/{@code y} 是 32 字节定长 base64url；</li>
 *   <li>签名在 JWS 里是 **{@code r||s} 各 32 字节定长**（不是 DER），BC 的 JCA 入口吃 DER，故此处转换；</li>
 *   <li>用户标识用 **GM/T 0003 默认值 {@code 1234567812345678}**（平台 {@code crypto.DefaultSM2UID} 同值）。</li>
 * </ul>
 *
 * <p>不做算法协商：只认 {@code SM3withSM2}，别的 alg 一律拒（防算法降级）。
 */
public final class Sm2 {

    /** 断言验签唯一允许的算法（契约 §4.1 M2）。 */
    public static final String ALG = "SM3withSM2";

    /** 曲线名（GM/T 0003）。 */
    public static final String CURVE = "sm2p256v1";

    private static final int COORD_BYTES = 32;
    private static final int SIG_BYTES = 64;

    static {
        if (Security.getProvider(BouncyCastleProvider.PROVIDER_NAME) == null) {
            Security.addProvider(new BouncyCastleProvider());
        }
    }

    private Sm2() {
    }

    /** 由 JWKS 的 {@code x}/{@code y}（32 字节定长大端）构造 SM2 公钥；点不在曲线上即拒。 */
    public static PublicKey publicKey(byte[] x, byte[] y) {
        if (x == null || y == null || x.length != COORD_BYTES || y.length != COORD_BYTES) {
            throw new IllegalArgumentException("SM2 坐标必须是 32 字节定长");
        }
        X9ECParameters params = GMNamedCurves.getByName(CURVE);
        BigInteger px = new BigInteger(1, x);
        BigInteger py = new BigInteger(1, y);
        if (px.signum() <= 0 || py.signum() <= 0) {
            throw new IllegalArgumentException("SM2 坐标非法（0）");
        }
        if (!params.getCurve().validatePoint(px, py).isValid()) {
            throw new IllegalArgumentException("SM2 公钥不在 " + CURVE + " 曲线上");
        }
        try {
            ECParameterSpec spec = new ECParameterSpec(params.getCurve(), params.getG(),
                    params.getN(), params.getH(), params.getSeed());
            ECPublicKeySpec keySpec = new ECPublicKeySpec(
                    params.getCurve().createPoint(px, py), spec);
            return KeyFactory.getInstance("EC", BouncyCastleProvider.PROVIDER_NAME)
                    .generatePublic(keySpec);
        } catch (Exception e) {
            throw new IllegalArgumentException("SM2 公钥构造失败", e);
        }
    }

    /** 用 {@code SM3withSM2} 验签；{@code signature} 是 JWS 里的 {@code r||s}（64 字节）。 */
    public static boolean verify(PublicKey publicKey, byte[] message, byte[] signature) {
        if (publicKey == null || message == null || signature == null || signature.length != SIG_BYTES) {
            return false;
        }
        try {
            Signature verifier = Signature.getInstance(ALG, BouncyCastleProvider.PROVIDER_NAME);
            verifier.initVerify(publicKey);
            verifier.update(message);
            return verifier.verify(rawToDer(signature));
        } catch (Exception e) {
            return false;
        }
    }

    /** {@code r||s}（各 32 字节）→ DER {@code SEQUENCE { INTEGER r, INTEGER s }}。 */
    public static byte[] rawToDer(byte[] raw) {
        if (raw == null || raw.length != SIG_BYTES) {
            throw new IllegalArgumentException("SM2 签名必须是 64 字节 r||s");
        }
        BigInteger r = new BigInteger(1, Arrays.copyOfRange(raw, 0, COORD_BYTES));
        BigInteger s = new BigInteger(1, Arrays.copyOfRange(raw, COORD_BYTES, SIG_BYTES));
        try {
            return new DERSequence(new ASN1Encodable[]{new ASN1Integer(r), new ASN1Integer(s)})
                    .getEncoded(ASN1Encoding.DER);
        } catch (Exception e) {
            throw new IllegalStateException("SM2 签名 DER 编码失败", e);
        }
    }
}
