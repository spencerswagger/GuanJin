package com.guanjin.spring.boot2;

import com.guanjin.core.authz.Authz;
import com.guanjin.core.authz.AuthzModel;
import com.guanjin.core.authz.RequirePermission;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.web.method.HandlerMethod;

import java.lang.reflect.Method;
import java.util.Arrays;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** GJ-CAP-14 {@code @RequirePermission} 的执行点：允许放行、拒绝 403，未声明注解的处理器不受影响。 */
class GuanjinAuthorizationInterceptorTest {

    private final GuanjinAuthorizationInterceptor interceptor = new GuanjinAuthorizationInterceptor();

    @BeforeEach
    void setUp() {
        Authz.init(AuthzModel.builder()
                        .role("order_admin").can("orders", "read", "write")
                        .role("order_viewer").can("orders", "read")
                        .defaultRole("user").can("profile", "read")
                        .build(),
                "classpath:/guanjin/model.conf");
        GuanjinContextHolder.clear();
    }

    @Test
    void allowedPermissionPasses() throws Exception {
        GuanjinContextHolder.set(principal("order_admin"));
        assertTrue(interceptor.preHandle(new MockHttpServletRequest("POST", "/orders"),
                new MockHttpServletResponse(), handlerMethod("write")));
    }

    @Test
    void deniedPermissionIs403InsufficientRole() throws Exception {
        GuanjinContextHolder.set(principal("order_viewer"));
        MockHttpServletResponse res = new MockHttpServletResponse();
        assertFalse(interceptor.preHandle(new MockHttpServletRequest("POST", "/orders"), res,
                handlerMethod("write")));
        assertEquals(403, res.getStatus());
        assertTrue(res.getContentAsString().contains("INSUFFICIENT_ROLE"), res.getContentAsString());
    }

    @Test
    void handlerWithoutAnnotationIsUntouched() throws Exception {
        GuanjinContextHolder.set(principal("order_viewer"));
        Method m = Controller.class.getMethod("unprotected");
        MockHttpServletResponse res = new MockHttpServletResponse();
        assertTrue(interceptor.preHandle(new MockHttpServletRequest("GET", "/health"),
                res, new HandlerMethod(new Controller(), m)));
        assertEquals(200, res.getStatus());
    }

    @Test
    void nonHandlerMethodObjectsAreIgnored() throws Exception {
        assertTrue(interceptor.preHandle(new MockHttpServletRequest("GET", "/static/app.js"),
                new MockHttpServletResponse(), new Object()));
    }

    @Test
    void unauthenticatedIsRejectedAs401() throws Exception {
        MockHttpServletResponse res = new MockHttpServletResponse();
        assertFalse(interceptor.preHandle(new MockHttpServletRequest("GET", "/orders"), res,
                handlerMethod("read")));
        assertEquals(401, res.getStatus(), "没有身份时不得进入授权判定");
        assertTrue(res.getContentAsString().contains("TOKEN_INVALID"), res.getContentAsString());
    }

    private static HandlerMethod handlerMethod(String action) throws Exception {
        Method m = "write".equals(action)
                ? Controller.class.getMethod("write")
                : Controller.class.getMethod("read");
        return new HandlerMethod(new Controller(), m);
    }

    private static GuanjinPrincipal principal(String role) {
        return new GuanjinPrincipal("user-1", "alice", Arrays.asList(role), "AAL2",
                Arrays.asList("pwd"), "orders", "jti-" + role);
    }

    /** 测试用控制器：注解在方法上（契约 §2.1 的形态）。 */
    static class Controller {

        @RequirePermission(resource = "orders", action = "read")
        public String read() {
            return "read";
        }

        @RequirePermission(resource = "orders", action = "write")
        public String write() {
            return "write";
        }

        public String unprotected() {
            return "ok";
        }
    }
}
