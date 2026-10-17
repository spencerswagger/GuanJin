package com.guanjin.sdk.spring;

import com.guanjin.sdk.AssertionVerifier;
import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinException;
import com.guanjin.sdk.GuanjinPrincipal;
import com.guanjin.sdk.replay.JtiReplayStore;
import java.io.IOException;
import java.util.Collections;
import java.util.Enumeration;
import java.util.List;
import java.util.stream.Collectors;
import javax.servlet.Filter;
import javax.servlet.FilterChain;
import javax.servlet.FilterConfig;
import javax.servlet.ServletException;
import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;
import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletRequestWrapper;
import javax.servlet.http.HttpServletResponse;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.security.core.context.SecurityContextHolder;

public class GuanjinAssertionFilter implements Filter {

    private static final Logger log = LoggerFactory.getLogger(GuanjinAssertionFilter.class);
    private static final String PROCESSED = GuanjinAssertionFilter.class.getName() + ".PROCESSED";

    private final GuanjinProperties properties;
    private final AssertionVerifier verifier;
    private final JtiReplayStore replayStore;
    private final GuanjinAuthenticationConverter authenticationConverter;
    private final PublicPathMatcher publicPathMatcher;

    public GuanjinAssertionFilter(GuanjinProperties properties,
                                  AssertionVerifier verifier,
                                  JtiReplayStore replayStore,
                                  GuanjinAuthenticationConverter authenticationConverter,
                                  PublicPathMatcher publicPathMatcher) {
        this.properties = properties;
        this.verifier = verifier;
        this.replayStore = replayStore;
        this.authenticationConverter = authenticationConverter;
        this.publicPathMatcher = publicPathMatcher;
    }

    @Override
    public void init(FilterConfig filterConfig) {
    }

    @Override
    public void destroy() {
    }

    @Override
    public void doFilter(ServletRequest request, ServletResponse response, FilterChain chain)
            throws IOException, ServletException {
        HttpServletRequest httpRequest = (HttpServletRequest) request;
        HttpServletResponse httpResponse = (HttpServletResponse) response;

        if (!properties.isEnabled()
                || publicPathMatcher.isPublic(httpRequest.getRequestURI(), httpRequest.getMethod())
                || httpRequest.getAttribute(PROCESSED) != null) {
            chain.doFilter(request, response);
            return;
        }
        httpRequest.setAttribute(PROCESSED, Boolean.TRUE);

        final String headerName = properties.effectiveAssertionHeader();

        // 1) 读 header；缺失 → 401（deny-by-default）
        String raw = httpRequest.getHeader(headerName);
        if (raw == null || raw.isEmpty()) {
            reject(httpResponse, HttpServletResponse.SC_UNAUTHORIZED, "missing " + headerName);
            return;
        }

        // 2) 转发前剥离：从进入 SDK 的第一刻起，下游再也拿不到该 header
        HttpServletRequest sanitized = new HeaderStrippingRequestWrapper(httpRequest, headerName);

        try {
            // 3) 验签（SM2，公钥来自 JWKS）+ 校验 iss/aud/exp/nbf
            GuanjinPrincipal principal = verifier.verify(raw);

            // 4) 重放检测：jti 在窗口内唯一
            replayStore.checkAndStore(principal.getJti(), principal.getExp());

            // 5) 身份写入请求上下文 + Spring Security
            GuanjinContextHolder.set(principal);
            if (properties.getSecurity().isInjectSecurityContext()) {
                SecurityContextHolder.getContext()
                        .setAuthentication(authenticationConverter.toAuthentication(principal));
            }

            try {
                chain.doFilter(sanitized, response);
            } finally {
                clearContexts();
            }
        } catch (GuanjinDependencyException e) {
            log.warn("关津内部令牌依赖不可用: {}", e.getMessage());
            clearContexts();
            reject(httpResponse, HttpServletResponse.SC_SERVICE_UNAVAILABLE, e.getMessage());
        } catch (GuanjinException e) {
            log.warn("关津内部令牌校验失败: {}", e.getMessage());
            clearContexts();
            reject(httpResponse, HttpServletResponse.SC_UNAUTHORIZED, e.getMessage());
        }
    }

    private void clearContexts() {
        GuanjinContextHolder.clear();
        SecurityContextHolder.clearContext();
    }

    private void reject(HttpServletResponse response, int status, String reason) throws IOException {
        response.setStatus(status);
        response.setContentType("application/json;charset=UTF-8");
        String safeReason = reason == null ? "" : reason.replace("\\", "").replace("\"", "'");
        response.getWriter().write("{\"error\":\"unauthorized\",\"reason\":\"" + safeReason + "\"}");
    }

    static final class HeaderStrippingRequestWrapper extends HttpServletRequestWrapper {

        private final String stripped;

        HeaderStrippingRequestWrapper(HttpServletRequest request, String stripped) {
            super(request);
            this.stripped = stripped;
        }

        @Override
        public String getHeader(String name) {
            return stripped.equalsIgnoreCase(name) ? null : super.getHeader(name);
        }

        @Override
        public Enumeration<String> getHeaders(String name) {
            return stripped.equalsIgnoreCase(name)
                    ? Collections.emptyEnumeration()
                    : super.getHeaders(name);
        }

        @Override
        public Enumeration<String> getHeaderNames() {
            List<String> names = Collections.list(super.getHeaderNames()).stream()
                    .filter(name -> !stripped.equalsIgnoreCase(name))
                    .collect(Collectors.toList());
            return Collections.enumeration(names);
        }
    }
}
