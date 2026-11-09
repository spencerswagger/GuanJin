package com.guanjin.core.publicpath;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;

/**
 * 公开路径判定（契约 GJ-CAP-19 / 配置 #24 #25）：**deny-by-default**。
 *
 * <p>命中即放行；未命中一律要求身份。内置白名单只含平台自身与静态资源，
 * **不含任何业务接口**——业务接口要公开必须显式声明（并在启动时打印供评审）。
 *
 * <p>模式语法（与 Go 侧同口径，够用即止）：`*` 匹配单段内任意字符；结尾 `/*` 匹配该前缀下
 * 任意深度；比较区分大小写（URL 路径本就区分）。
 */
public final class PathMatcher {

    /** 内置白名单（契约 #25 默认 true）。 */
    public static final List<String> BUILTIN_PUBLIC_PATHS = Collections.unmodifiableList(Arrays.asList(
            "/healthz",
            "/livez",
            "/readyz",
            "/error",
            "/favicon.ico",
            "/robots.txt"));

    private final List<String> patterns;

    public PathMatcher(List<String> declared, boolean includeBuiltin) {
        List<String> pats = new ArrayList<String>();
        if (includeBuiltin) {
            pats.addAll(BUILTIN_PUBLIC_PATHS);
        }
        if (declared != null) {
            for (String p : declared) {
                if (p != null && !p.trim().isEmpty()) {
                    pats.add(p.trim());
                }
            }
        }
        this.patterns = Collections.unmodifiableList(pats);
    }

    /** 是否公开（方法不参与判定：公开即整条路径公开）。 */
    public boolean isPublic(String path) {
        String p = normalize(path);
        for (String pattern : patterns) {
            if (matches(pattern, p)) {
                return true;
            }
        }
        return false;
    }

    /** 生效的模式清单（启动自检打印它，GJ-CAP-22）。 */
    public List<String> patterns() {
        return patterns;
    }

    private static String normalize(String p) {
        if (p == null || p.isEmpty()) {
            return "/";
        }
        String v = p.startsWith("/") ? p : "/" + p;
        if (v.length() > 1 && v.endsWith("/")) {
            v = v.substring(0, v.length() - 1);
        }
        return v;
    }

    private static boolean matches(String pattern, String path) {
        String pat = normalize(pattern);
        if (pat.equals(path)) {
            return true;
        }
        if (pat.endsWith("/*")) {
            String prefix = pat.substring(0, pat.length() - 2);
            return path.equals(prefix) || path.startsWith(prefix + "/");
        }
        if (pat.endsWith("*")) {
            return path.startsWith(pat.substring(0, pat.length() - 1));
        }
        return false;
    }
}
