package com.guanjin.sdk.crypto;

import org.bouncycastle.crypto.digests.SM3Digest;
import org.bouncycastle.crypto.macs.HMac;
import org.bouncycastle.crypto.params.KeyParameter;

public final class GuanjinSm3 {

    public static final int DIGEST_SIZE = 32;

    private GuanjinSm3() {
    }

    public static byte[] digest(byte[] data) {
        SM3Digest digest = new SM3Digest();
        digest.update(data, 0, data.length);
        byte[] out = new byte[DIGEST_SIZE];
        digest.doFinal(out, 0);
        return out;
    }

    public static byte[] hmac(byte[] key, byte[] message) {
        HMac mac = new HMac(new SM3Digest());
        mac.init(new KeyParameter(key));
        mac.update(message, 0, message.length);
        byte[] out = new byte[mac.getMacSize()];
        mac.doFinal(out, 0);
        return out;
    }
}
