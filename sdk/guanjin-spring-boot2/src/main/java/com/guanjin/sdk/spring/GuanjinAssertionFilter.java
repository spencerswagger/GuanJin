package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinAssertionVerifier;
import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinException;
import com.guanjin.sdk.GuanjinIdentity;
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
    private static final String IDENTITY_ATTRIBUTE = GuanjinAssertionFilter.class.getName() + ".IDENTITY";

    private final GuanjinProperties properties;
    private final GuanjinAssertionVerifier verifier;
    private final GuanjinAuthenticationConverter authenticationConverter;
    private final GuanjinPublicPathMatcher publicPathMatcher;

    public GuanjinAssertionFilter(GuanjinProperties properties,
                                  GuanjinAssertionVerifier verifier,
                                  GuanjinAuthenticationConverter authenticationConverter,
                                  GuanjinPublicPathMatcher publicPathMatcher) {
        this.properties = properties;
        this.verifier = verifier;
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
                || publicPathMatcher.isPublic(httpRequest.getRequestURI(), httpRequest.getMethod())) {
            chain.doFilter(request, response);
            return;
        }

        Object existing = httpRequest.getAttribute(IDENTITY_ATTRIBUTE);
        if (existing instanceof GuanjinIdentity) {
            install((GuanjinIdentity) existing);
            try {
                chain.doFilter(request, response);
            } finally {
                clearContexts();
            }
            return;
        }

        final String headerName = properties.effectiveAssertionHeader();
        String raw = httpRequest.getHeader(headerName);
        if (raw == null || raw.isEmpty()) {
            reject(httpResponse, HttpServletResponse.SC_UNAUTHORIZED, "missing " + headerName);
            return;
        }

        HttpServletRequest sanitized = new HeaderStrippingRequestWrapper(httpRequest, headerName);

        try {
            GuanjinIdentity identity = verifier.verify(raw);
            httpRequest.setAttribute(IDENTITY_ATTRIBUTE, identity);
            install(identity);
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

    private void install(GuanjinIdentity identity) {
        GuanjinContextHolder.set(identity);
        if (properties.getSecurity().isInjectSecurityContext()) {
            SecurityContextHolder.getContext()
                    .setAuthentication(authenticationConverter.toAuthentication(identity));
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
