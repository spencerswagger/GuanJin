package com.guanjin.core.http;

import java.net.URI;
import java.util.Map;

/**
 * 出站 HTTP 抽象（契约 §5.1.3）：core 不能依赖 Spring 的 RestTemplate，JDK 8 又没有
 * {@code java.net.http}，所以只定接口 + 默认 {@link UrlConnectionHttpFetcher}。
 *
 * <p>只有 3 个出站端点（契约 §2.1.1）：JWKS / agent report / agent policies。
 * 本切片用到 JWKS 与 policies（后者默认关闭，见 {@code agent.pull.enabled}）。
 */
public interface HttpFetcher {

    /** 发一个 GET。**超时/非 2xx 都由调用方决定语义**，这里只如实返回状态码。 */
    HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException;
}
