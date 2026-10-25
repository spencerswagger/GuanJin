package com.guanjin.sdk.spring;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collection;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import org.springframework.util.AntPathMatcher;

public class GuanjinPublicPathMatcher {

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
    private final CopyOnWriteArrayList<String> patterns = new CopyOnWriteArrayList<>();

    public GuanjinPublicPathMatcher(List<String> declaredPaths, boolean includeBuiltin) {
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

    public void addPublicPaths(Collection<String> paths) {
        if (paths == null) {
            return;
        }
        for (String path : paths) {
            if (path != null && !path.isEmpty() && !patterns.contains(path)) {
                patterns.add(path);
            }
        }
    }

    public List<String> patterns() {
        return new ArrayList<>(patterns);
    }
}
