package com.guanjin.spring.boot2;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.Charset;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * 假平台（真 HTTP 服务，JDK 自带 {@link HttpServer}）：按路径**计数**，供"零调用"与"首启命中 JWKS"取证。
 *
 * <p>为什么不用 mock 框架：AC-SDK-10 要求"用假服务端计数，不许估算"，计数必须来自真实的
 * socket 请求；JWKS 的 404 也必须是真的 404 响应。
 */
public final class TestPlatform implements AutoCloseable {

    private static final Charset UTF8 = Charset.forName("UTF-8");

    /** 平台真实形态的 JWKS（000013 CHECK / keygen.go:44-47 / D29）：oidc（alg=SM3withSM2）+ gateway（alg=SM2）。 */
    public static final String REAL_JWKS = "{\"keys\":["
            + "{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"oidc-2026-11\",\"use\":\"sig\",\"alg\":\"SM3withSM2\","
            + "\"x\":\"JgXKQ1n0a2Zk9Q8m1oQbYw\",\"y\":\"Zt7b1s0hVn2m5cQ9xJ8kLw\"},"
            + "{\"kty\":\"EC\",\"crv\":\"SM2\",\"kid\":\"gw-2026-11\",\"use\":\"sig\",\"alg\":\"SM2\","
            + "\"x\":\"Qk9m2v1s0hVn5cQ8xJ7kLw\",\"y\":\"JgXKQ1n0a2Zk9Q8m1oQbYw\"}"
            + "]}";

    private final HttpServer server;
    private final Map<String, AtomicInteger> hits = new ConcurrentHashMap<String, AtomicInteger>();
    private volatile int jwksStatus = 200;
    private volatile String jwksBody = REAL_JWKS;

    public TestPlatform() throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/.well-known/jwks.json", new HttpHandler() {
            @Override
            public void handle(HttpExchange exchange) throws IOException {
                count("/.well-known/jwks.json");
                respond(exchange, jwksStatus, jwksBody);
            }
        });
        server.createContext("/api/v1/agent/policies", new HttpHandler() {
            @Override
            public void handle(HttpExchange exchange) throws IOException {
                count("/api/v1/agent/policies");
                respond(exchange, 200, "{\"version\":1,\"policies\":[]}");
            }
        });
        server.createContext("/api/v1/agent/report", new HttpHandler() {
            @Override
            public void handle(HttpExchange exchange) throws IOException {
                count("/api/v1/agent/report");
                respond(exchange, 200, "{}");
            }
        });
        server.setExecutor(null);
        server.start();
    }

    public String baseUrl() {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    public void jwksStatus(int status) {
        this.jwksStatus = status;
    }

    public void jwksBody(String body) {
        this.jwksBody = body;
    }

    /** 某路径被真实请求过几次。 */
    public int hits(String path) {
        AtomicInteger c = hits.get(path);
        return c == null ? 0 : c.get();
    }

    public void resetCounters() {
        hits.clear();
    }

    @Override
    public void close() {
        server.stop(0);
    }

    private void count(String path) {
        AtomicInteger c = hits.get(path);
        if (c == null) {
            hits.putIfAbsent(path, new AtomicInteger());
            c = hits.get(path);
        }
        c.incrementAndGet();
    }

    private static void respond(HttpExchange exchange, int status, String body) throws IOException {
        byte[] bytes = body.getBytes(UTF8);
        exchange.getResponseHeaders().set("Content-Type", "application/json");
        exchange.sendResponseHeaders(status, bytes.length);
        OutputStream out = exchange.getResponseBody();
        out.write(bytes);
        out.close();
    }
}
