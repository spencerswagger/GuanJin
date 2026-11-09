package com.guanjin.core.http;

import java.nio.charset.Charset;

/** 一次 HTTP 应答的**原始**结果：状态码 + 响应体字节 + Content-Type。 */
public final class HttpResult {

    private static final Charset UTF_8 = Charset.forName("UTF-8");

    private final int status;
    private final byte[] body;
    private final String contentType;

    public HttpResult(int status, byte[] body, String contentType) {
        this.status = status;
        this.body = body == null ? new byte[0] : body;
        this.contentType = contentType;
    }

    public int status() {
        return status;
    }

    public byte[] body() {
        return body;
    }

    public String contentType() {
        return contentType;
    }

    public String bodyAsString() {
        return new String(body, UTF_8);
    }

    public boolean is2xx() {
        return status >= 200 && status < 300;
    }

    @Override
    public String toString() {
        return "HttpResult{status=" + status + ", bytes=" + body.length + "}";
    }
}
