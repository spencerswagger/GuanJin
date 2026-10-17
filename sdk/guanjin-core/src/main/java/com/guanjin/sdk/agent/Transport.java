package com.guanjin.sdk.agent;

import java.security.MessageDigest;
import java.util.Map;

public interface Transport {

    String post(String url, Map<String, String> headers, String body) throws Exception;

    String get(String url, Map<String, String> headers) throws Exception;

    static String sha256Hex(byte[] data) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] out = digest.digest(data);
            StringBuilder sb = new StringBuilder(out.length * 2);
            for (byte b : out) {
                sb.append(Character.forDigit((b >> 4) & 0xF, 16));
                sb.append(Character.forDigit(b & 0xF, 16));
            }
            return sb.toString();
        } catch (Exception e) {
            throw new IllegalStateException("SHA-256 不可用", e);
        }
    }
}
