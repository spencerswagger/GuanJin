package com.guanjin.sdk.spring;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import org.springframework.util.AntPathMatcher;

public class PublicPathMatcher {

    private static final List<String> BUILTIN = Arrays.asList(
            "/health",
            "/healthz",
            "/readyz",
            "/error",
            "/favicon.ico",
            "/actuator/**",
            "/v3/api-docs/**",
            "/swagger-ui/**",
            "/swagger-ui.html",
            "/webjars/**");

    private final AntPathMatcher matcher = new AntPathMatcher();
    private final List<String> patterns = new ArrayList<>();

    public PublicPathMatcher(List<String> declaredPaths, boolean includeBuiltin) {
        if (declaredPaths != null) {
            patterns.addAll(declaredPaths);
        }
        if (includeBuiltin) {
            patterns.addAll(BUILTIN);
        }
    }

    public boolean isPublic(String uri, String method) {
        if (uri == null) {
            return false;
        }
        for (String pattern : patterns) {
            if (matcher.match(pattern, uri)) {
                return true;
            }
        }
        return false;
    }

    public List<String> patterns() {
        return new ArrayList<>(patterns);
    }
}
