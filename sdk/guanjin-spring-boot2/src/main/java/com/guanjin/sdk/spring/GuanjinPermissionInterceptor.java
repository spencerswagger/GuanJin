package com.guanjin.sdk.spring;

import com.guanjin.sdk.authz.GuanjinAuthzEngine;
import java.io.IOException;
import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletResponse;
import org.springframework.core.annotation.AnnotatedElementUtils;
import org.springframework.web.method.HandlerMethod;
import org.springframework.web.servlet.HandlerInterceptor;

public class GuanjinPermissionInterceptor implements HandlerInterceptor {

    private final GuanjinAuthzEngine authzEngine;

    public GuanjinPermissionInterceptor(GuanjinAuthzEngine authzEngine) {
        this.authzEngine = authzEngine;
    }

    @Override
    public boolean preHandle(HttpServletRequest request, HttpServletResponse response, Object handler)
            throws IOException {
        if (!(handler instanceof HandlerMethod)) {
            return true;
        }
        HandlerMethod handlerMethod = (HandlerMethod) handler;
        RequirePermission annotation = AnnotatedElementUtils.findMergedAnnotation(
                handlerMethod.getMethod(), RequirePermission.class);
        if (annotation == null) {
            annotation = AnnotatedElementUtils.findMergedAnnotation(
                    handlerMethod.getBeanType(), RequirePermission.class);
        }
        if (annotation == null) {
            return true;
        }
        if (authzEngine.check(annotation.resource(), annotation.action())) {
            return true;
        }
        response.setStatus(HttpServletResponse.SC_FORBIDDEN);
        response.setContentType("application/json;charset=UTF-8");
        response.getWriter().write("{\"error\":\"forbidden\",\"resource\":\""
                + annotation.resource() + "\",\"action\":\"" + annotation.action() + "\"}");
        return false;
    }
}
