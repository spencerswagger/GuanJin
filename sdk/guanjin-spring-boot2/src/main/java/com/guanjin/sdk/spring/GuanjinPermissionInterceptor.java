package com.guanjin.sdk.spring;

import com.guanjin.sdk.authz.AuthzEngine;
import org.aopalliance.intercept.MethodInterceptor;
import org.aopalliance.intercept.MethodInvocation;
import org.springframework.core.annotation.AnnotatedElementUtils;
import org.springframework.security.access.AccessDeniedException;

public class GuanjinPermissionInterceptor implements MethodInterceptor {

    private final AuthzEngine authzEngine;

    public GuanjinPermissionInterceptor(AuthzEngine authzEngine) {
        this.authzEngine = authzEngine;
    }

    @Override
    public Object invoke(MethodInvocation invocation) throws Throwable {
        RequirePermission annotation = AnnotatedElementUtils.findMergedAnnotation(
                invocation.getMethod(), RequirePermission.class);
        if (annotation == null) {
            return invocation.proceed();
        }
        if (!authzEngine.check(annotation.resource(), annotation.action())) {
            throw new AccessDeniedException(
                    "无权访问 " + annotation.resource() + ":" + annotation.action());
        }
        return invocation.proceed();
    }
}
