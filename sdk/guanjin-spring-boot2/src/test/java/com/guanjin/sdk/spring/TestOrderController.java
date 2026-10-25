package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinIdentity;
import java.util.LinkedHashMap;
import java.util.Map;
import javax.servlet.http.HttpServletRequest;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class TestOrderController {

    @RequirePermission(resource = "orders", action = "read")
    @GetMapping("/api/orders/{id}")
    public Map<String, Object> read(@PathVariable String id, HttpServletRequest request) {
        GuanjinIdentity identity = GuanjinContextHolder.get();
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("orderId", id);
        body.put("sub", identity == null ? null : identity.getSub());
        body.put("roles", identity == null ? null : identity.getRoles());
        body.put("acr", identity == null ? null : identity.getAcr());
        body.put("downstreamHeader", request.getHeader("X-GuanJin-Assertion"));
        body.put("downstreamHeaderNamesContainAssertion",
                java.util.Collections.list(request.getHeaderNames()).stream()
                        .anyMatch(name -> name.equalsIgnoreCase("X-GuanJin-Assertion")));
        return body;
    }

    @RequirePermission(resource = "orders", action = "write")
    @PostMapping("/api/orders/{id}")
    public Map<String, Object> write(@PathVariable String id) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("orderId", id);
        body.put("written", true);
        return body;
    }

    @PublicApi
    @GetMapping("/api/public/ping")
    public Map<String, Object> ping() {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("pong", true);
        return body;
    }

    @org.springframework.security.access.prepost.PreAuthorize("hasRole('ADMIN')")
    @GetMapping("/api/admin")
    public Map<String, Object> admin() {
        Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("admin", true);
        body.put("authorities", authentication == null ? null : authentication.getAuthorities());
        return body;
    }

    @GetMapping("/api/headers")
    public Map<String, Object> headers(HttpServletRequest request) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("assertionHeader", request.getHeader("X-GuanJin-Assertion"));
        body.put("identitySub", GuanjinContextHolder.get() == null ? null : GuanjinContextHolder.get().getSub());
        return body;
    }
}
