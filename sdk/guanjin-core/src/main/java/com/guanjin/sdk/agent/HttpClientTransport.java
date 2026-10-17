package com.guanjin.sdk.agent;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.Map;

public final class HttpClientTransport implements Transport {

    private final HttpClient client;
    private final Duration requestTimeout;

    public HttpClientTransport() {
        this(Duration.ofSeconds(5));
    }

    public HttpClientTransport(Duration requestTimeout) {
        this.requestTimeout = requestTimeout;
        this.client = HttpClient.newBuilder().connectTimeout(requestTimeout).build();
    }

    @Override
    public String post(String url, Map<String, String> headers, String body) throws Exception {
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(url))
                .timeout(requestTimeout)
                .POST(HttpRequest.BodyPublishers.ofString(body));
        headers.forEach(builder::header);
        HttpResponse<String> response = client.send(builder.build(), HttpResponse.BodyHandlers.ofString());
        return check(response);
    }

    @Override
    public String get(String url, Map<String, String> headers) throws Exception {
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(url))
                .timeout(requestTimeout)
                .GET();
        headers.forEach(builder::header);
        HttpResponse<String> response = client.send(builder.build(), HttpResponse.BodyHandlers.ofString());
        return check(response);
    }

    private String check(HttpResponse<String> response) {
        int status = response.statusCode();
        if (status < 200 || status >= 300) {
            throw new IllegalStateException("Agent 面请求失败，HTTP " + status + ": " + response.body());
        }
        return response.body();
    }
}
