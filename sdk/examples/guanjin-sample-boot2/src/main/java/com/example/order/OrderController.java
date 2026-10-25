package com.example.order;

import com.guanjin.sdk.GuanjinIdentity;
import com.guanjin.sdk.spring.GuanjinContextHolder;
import com.guanjin.sdk.spring.RequirePermission;
import java.util.LinkedHashMap;
import java.util.Map;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class OrderController {

    @RequirePermission(resource = "orders", action = "read")
    @GetMapping("/api/orders/{id}")
    public Map<String, Object> get(@PathVariable String id) {
        GuanjinIdentity identity = GuanjinContextHolder.get();
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("orderId", id);
        body.put("sub", identity == null ? null : identity.getSub());
        body.put("roles", identity == null ? null : identity.getRoles());
        return body;
    }

    @PreAuthorize("hasRole('ADMIN')")
    @GetMapping("/api/admin")
    public Map<String, Object> admin() {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("admin", true);
        return body;
    }
}
