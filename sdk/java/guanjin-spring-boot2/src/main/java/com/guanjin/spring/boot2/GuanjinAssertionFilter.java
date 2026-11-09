package com.guanjin.spring.boot2;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.assertion.Assertion;
import com.guanjin.core.assertion.AssertionVerifier;
import com.guanjin.core.assertion.ClaimValidator;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.ReplayGuard;
import org.springframework.core.Ordered;

import javax.servlet.Filter;
import javax.servlet.FilterChain;
import javax.servlet.ServletException;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 断言过滤器：**契约 D10 的唯一六步时序**（禁止换序）。
 *
 * <pre>
 * ① 读原始头（此时不删，它是唯一凭证来源）
 * ② 验签（SM2 + JWKS 缓存 + 未知 kid 强刷一次）          —— AssertionVerifier SPI
 * ③ 校验 claim（exp/iat 偏移、iss、aud、jti 防重放）
 * ④ 注入上下文（GuanjinContextHolder / 请求属性）
 * ⑤ 替换请求视图（wrapper 覆写 getHeader/getHeaders/getHeaderNames，不是"删 header"）
 * ⑥ 业务 handler 运行
 * </pre>
 *
 * <p>禁止两种错法：① 在步① 就删头（无令牌可验）；② 只在下游转发前删一次（框架/日志已缓存 header 名）。
 *
 * <p>失败一律 fail-closed：401 {@code TOKEN_INVALID} / 503 {@code KEY_UNAVAILABLE} /
 * 503 {@code REDIS_UNAVAILABLE}，**没有放行分支**。
 */
public class GuanjinAssertionFilter implements Filter {

    /** 过滤器顺序：在 Spring Security 之前（契约 §5.1 接入说明）。 */
    public static final int ORDER = Ordered.HIGHEST_PRECEDENCE + 100;

    /** 身份在请求属性里的键（GJ-CAP-10 的非 Spring 落点之一）。 */
    public static final String PRINCIPAL_ATTRIBUTE = "guanjin.principal";

    private final GuanjinProperties properties;
    private final AssertionVerifier verifier;
    private final ReplayGuard replayGuard;
    private final PathMatcher pathMatcher;
    private final SecurityContextInjector contextInjector;
    private final ObjectMapper json = new ObjectMapper();

    public GuanjinAssertionFilter(GuanjinProperties properties, AssertionVerifier verifier,
                                  ReplayGuard replayGuard, PathMatcher pathMatcher) {
        this(properties, verifier, replayGuard, pathMatcher, null);
    }

    public GuanjinAssertionFilter(GuanjinProperties properties, AssertionVerifier verifier,
                                  ReplayGuard replayGuard, PathMatcher pathMatcher,
                                  SecurityContextInjector contextInjector) {
        this.properties = properties;
        this.verifier = verifier;
        this.replayGuard = replayGuard;
        this.pathMatcher = pathMatcher;
        this.contextInjector = contextInjector == null ? SecurityContextInjector.NOOP : contextInjector;
    }

    @Override
    public void doFilter(ServletRequest request, ServletResponse response, FilterChain chain)
            throws IOException, ServletException {
        if (!(request instanceof HttpServletRequest) || !(response instanceof HttpServletResponse)) {
            chain.doFilter(request, response);
            return;
        }
        HttpServletRequest req = (HttpServletRequest) request;
        HttpServletResponse res = (HttpServletResponse) response;

        // 公开路径：显式声明才放行（deny-by-default，GJ-CAP-19）
        if (pathMatcher.isPublic(pathWithinApp(req))) {
            chain.doFilter(req, res);
            return;
        }

        String headerName = properties.getGateway().getAssertionHeader();
        try {
            // ① 读原始头（不删）
            String raw = req.getHeader(headerName);
            if (raw == null || raw.trim().isEmpty()) {
                throw GuanjinSdkException.tokenInvalid("缺少 " + headerName + " 头");
            }
            // ② 验签
            Assertion assertion = verifier.verify(raw.trim());
            // ③ claim 校验 + jti 防重放（401 TOKEN_INVALID / 503 REDIS_UNAVAILABLE）
            ClaimValidator.validate(assertion, properties);
            replayGuard.firstUse(assertion.jti(), assertion.expiresAt());
            // ④ 注入上下文
            GuanjinPrincipal principal = GuanjinPrincipal.from(assertion);
            GuanjinContextHolder.set(principal);
            req.setAttribute(PRINCIPAL_ATTRIBUTE, principal);
            // ④′ 宿主上下文（GJ-CAP-10）：Spring Security 在 classpath 且 inject-security-context=true 时写 SecurityContext；
            // 断言原文不进 principal/credentials（业务与日志都拿不到令牌）
            contextInjector.inject(principal);
            // ⑤ 替换请求视图 → ⑥ handler
            chain.doFilter(new HeaderStrippingRequestWrapper(req, headerName), res);
        } catch (GuanjinSdkException e) {
            writeRejection(res, e);
        } finally {
            contextInjector.clear();
            GuanjinContextHolder.clear();
        }
    }

    private void writeRejection(HttpServletResponse res, GuanjinSdkException e) throws IOException {
        res.setStatus(e.getStatus());
        res.setContentType("application/json;charset=UTF-8");
        Map<String, Object> body = new LinkedHashMap<String, Object>();
        body.put("code", e.getCode());
        // message 面向人、不含敏感信息（令牌/secret 不进响应，诊断靠日志里的 requestId）
        body.put("message", e.getMessage());
        json.writeValue(res.getOutputStream(), body);
        res.flushBuffer();
    }

    /** 应用内路径（去掉 contextPath），用于公开路径匹配。 */
    private static String pathWithinApp(HttpServletRequest req) {
        String uri = req.getRequestURI();
        String ctx = req.getContextPath();
        if (ctx != null && !ctx.isEmpty() && uri != null && uri.startsWith(ctx)) {
            uri = uri.substring(ctx.length());
        }
        return uri == null || uri.isEmpty() ? "/" : uri;
    }
}
