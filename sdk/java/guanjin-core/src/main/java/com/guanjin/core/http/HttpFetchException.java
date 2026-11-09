package com.guanjin.core.http;

/** 出站请求的传输层失败（连不上 / 超时 / 读失败）。语义由调用方决定（fail-closed）。 */
public class HttpFetchException extends Exception {

    private static final long serialVersionUID = 1L;

    public HttpFetchException(String message, Throwable cause) {
        super(message, cause);
    }
}
