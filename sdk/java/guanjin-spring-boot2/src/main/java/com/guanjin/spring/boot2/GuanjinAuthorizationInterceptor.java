package com.guanjin.spring.boot2;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.authz.Authz;
import com.guanjin.core.authz.RequirePermission;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.springframework.web.method.HandlerMethod;
import org.springframework.web.servlet.HandlerInterceptor;

import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * {@code @RequirePermission} 的执行点（契约 GJ-CAP-14）。
 *
 * <p>只对**真正命中**的处理器判定：静态资源 / 无注解的处理器直接放行（无注解 = 不声明权限，
 * 但验签与身份注入仍由过滤器负责，不受影响）。
 *
 * <p>判定失败一律 403 {@code INSUFFICIENT_ROLE}（契约 §4.4），不写"原因细节"到响应体
 * （避免把角色/资源探测面暴露给调用方；诊断靠日志）。
 */
public class GuanjinAuthorizationInterceptor implements HandlerInterceptor {

    private final ObjectMapper json = new ObjectMapper();

    @Override
    public boolean preHandle(HttpServletRequest request, HttpServletResponse response, Object handler)
            throws IOException {
        if (!(handler instanceof HandlerMethod)) {
            return true;
        }
        HandlerMethod method = (HandlerMethod) handler;
        RequirePermission required = method.getMethodAnnotation(RequirePermission.class);
        if (required == null) {
            required = method.getBeanType().getAnnotation(RequirePermission.class);
        }
        if (required == null) {
            return true;
        }
        GuanjinPrincipal principal = GuanjinContextHolder.current();
        try {
            Authz.require(principal, required.resource(), required.action());
            return true;
        } catch (GuanjinSdkException e) {
            writeRejection(response, e);
            return false;
        }
    }

    private void writeRejection(HttpServletResponse response, GuanjinSdkException e) throws IOException {
        response.setStatus(e.getStatus());
        response.setContentType("application/json;charset=UTF-8");
        Map<String, Object> body = new LinkedHashMap<String, Object>();
        body.put("code", e.getCode());
        body.put("message", e.getMessage());
        json.writeValue(response.getOutputStream(), body);
        response.flushBuffer();
    }
}
