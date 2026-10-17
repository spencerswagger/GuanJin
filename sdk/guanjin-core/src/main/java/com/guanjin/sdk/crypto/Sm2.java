package com.guanjin.sdk.crypto;

import com.guanjin.sdk.GuanjinAuthenticationException;
import java.math.BigInteger;
import java.security.SecureRandom;
import org.bouncycastle.asn1.ASN1EncodableVector;
import org.bouncycastle.asn1.ASN1Integer;
import org.bouncycastle.asn1.ASN1Primitive;
import org.bouncycastle.asn1.ASN1Sequence;
import org.bouncycastle.asn1.DERSequence;
import org.bouncycastle.asn1.gm.GMNamedCurves;
import org.bouncycastle.asn1.x9.X9ECParameters;
import org.bouncycastle.math.ec.ECPoint;

public final class Sm2 {

    private static final X9ECParameters PARAMS = GMNamedCurves.getByName("sm2p256v1");
    private static final BigInteger ONE = BigInteger.ONE;
    private static final SecureRandom RANDOM = new SecureRandom();

    private Sm2() {
    }

    public static X9ECParameters params() {
        return PARAMS;
    }

    public static BigInteger n() {
        return PARAMS.getN();
    }

    public static ECPoint publicKey(byte[] x, byte[] y) {
        return publicKey(new BigInteger(1, x), new BigInteger(1, y));
    }

    public static ECPoint publicKey(BigInteger x, BigInteger y) {
        ECPoint point;
        try {
            point = PARAMS.getCurve().createPoint(x, y);
        } catch (RuntimeException e) {
            throw new GuanjinAuthenticationException("SM2 公钥无效: " + e.getMessage(), e);
        }
        if (!point.isValid()) {
            throw new GuanjinAuthenticationException("SM2 公钥不在曲线上");
        }
        return point.normalize();
    }

    public static ECPoint publicKeyFromPrivate(BigInteger d) {
        return PARAMS.getG().multiply(d).normalize();
    }

    public static boolean verify(ECPoint pub, byte[] digest, byte[] signatureDer) {
        if (pub == null || digest == null || digest.length != Sm3.DIGEST_SIZE || signatureDer == null) {
            return false;
        }
        BigInteger r;
        BigInteger s;
        try {
            ASN1Sequence seq = ASN1Sequence.getInstance(ASN1Primitive.fromByteArray(signatureDer));
            if (seq.size() != 2) {
                return false;
            }
            r = ASN1Integer.getInstance(seq.getObjectAt(0)).getPositiveValue();
            s = ASN1Integer.getInstance(seq.getObjectAt(1)).getPositiveValue();
        } catch (Exception e) {
            return false;
        }
        return verify(pub, digest, r, s);
    }

    public static boolean verify(ECPoint pub, byte[] digest, BigInteger r, BigInteger s) {
        BigInteger n = n();
        if (r == null || s == null) {
            return false;
        }
        if (r.signum() <= 0 || r.compareTo(n) >= 0 || s.signum() <= 0 || s.compareTo(n) >= 0) {
            return false;
        }
        BigInteger e = new BigInteger(1, digest);
        BigInteger t = r.add(s).mod(n);
        if (t.signum() == 0) {
            return false;
        }
        ECPoint p1 = PARAMS.getG().multiply(s);
        ECPoint p2 = pub.multiply(t);
        ECPoint p = p1.add(p2).normalize();
        BigInteger x = p.getAffineXCoord().toBigInteger();
        BigInteger v = e.add(x).mod(n);
        return v.equals(r);
    }

    public static byte[] sign(BigInteger d, byte[] digest) {
        if (d == null || d.signum() <= 0) {
            throw new GuanjinAuthenticationException("SM2 私钥无效");
        }
        if (digest == null || digest.length != Sm3.DIGEST_SIZE) {
            throw new GuanjinAuthenticationException("SM2 待签摘要必须为 32 字节");
        }
        BigInteger n = n();
        BigInteger e = new BigInteger(1, digest);
        while (true) {
            BigInteger k = new BigInteger(n.bitLength(), RANDOM).mod(n.subtract(ONE)).add(ONE);
            ECPoint kG = PARAMS.getG().multiply(k).normalize();
            BigInteger x1 = kG.getAffineXCoord().toBigInteger();
            BigInteger r = e.add(x1).mod(n);
            if (r.signum() == 0 || r.add(k).equals(n)) {
                continue;
            }
            BigInteger s = k.subtract(r.multiply(d)).multiply(d.add(ONE).modInverse(n)).mod(n);
            if (s.signum() == 0) {
                continue;
            }
            return encodeSignature(r, s);
        }
    }

    public static byte[] encodeSignature(BigInteger r, BigInteger s) {
        ASN1EncodableVector vector = new ASN1EncodableVector();
        vector.add(new ASN1Integer(r));
        vector.add(new ASN1Integer(s));
        try {
            return new DERSequence(vector).getEncoded("DER");
        } catch (java.io.IOException ex) {
            throw new GuanjinAuthenticationException("SM2 签名编码失败: " + ex.getMessage(), ex);
        }
    }
}
