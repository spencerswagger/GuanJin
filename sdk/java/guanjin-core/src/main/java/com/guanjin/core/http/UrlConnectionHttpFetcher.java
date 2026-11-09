package com.guanjin.core.http;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.util.Map;

/**
 * 默认 {@link HttpFetcher}：JDK 8 的 {@link HttpURLConnection}（零第三方依赖）。
 *
 * <p>注意 JDK 8 黑名单（契约 §5.1.1）：不用 {@code java.net.http.*}（11+）、
 * 不用 {@code InputStream.readAllBytes()}（9+）——这里用 8K 缓冲循环读。
 *
 * <p>超时即失败，不重试后放行（契约 #3/#4）。
 */
public final class UrlConnectionHttpFetcher implements HttpFetcher {

    private static final int BUFFER = 8192;

    private final Duration connectTimeout;
    private final Duration readTimeout;

    public UrlConnectionHttpFetcher(Duration connectTimeout, Duration readTimeout) {
        this.connectTimeout = connectTimeout;
        this.readTimeout = readTimeout;
    }

    @Override
    public HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException {
        HttpURLConnection conn = null;
        try {
            URL url = uri.toURL();
            conn = (HttpURLConnection) url.openConnection();
            conn.setRequestMethod("GET");
            conn.setConnectTimeout(millis(connectTimeout));
            conn.setReadTimeout(millis(readTimeout));
            conn.setInstanceFollowRedirects(false);
            conn.setRequestProperty("Accept", "application/json");
            if (headers != null) {
                for (Map.Entry<String, String> e : headers.entrySet()) {
                    conn.setRequestProperty(e.getKey(), e.getValue());
                }
            }
            int status = conn.getResponseCode();
            InputStream in = status >= 400 ? conn.getErrorStream() : conn.getInputStream();
            byte[] body = in == null ? new byte[0] : readFully(in);
            return new HttpResult(status, body, conn.getContentType());
        } catch (IOException e) {
            throw new HttpFetchException("GET " + uri + " 失败（连接/读取超时即失败，不重试放行）", e);
        } finally {
            if (conn != null) {
                conn.disconnect();
            }
        }
    }

    private static int millis(Duration d) {
        if (d == null) {
            return 0;
        }
        long ms = d.toMillis();
        return ms > Integer.MAX_VALUE ? Integer.MAX_VALUE : (int) ms;
    }

    private static byte[] readFully(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream(4096);
        byte[] buf = new byte[BUFFER];
        int n;
        while ((n = in.read(buf)) >= 0) {
            out.write(buf, 0, n);
        }
        in.close();
        return out.toByteArray();
    }
}
