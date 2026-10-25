package com.guanjin.sdk.authz;

import java.util.Collections;
import java.util.Map;
import java.util.Set;
import com.googlecode.aviator.runtime.function.FunctionUtils;
import com.googlecode.aviator.runtime.type.AviatorBoolean;
import com.googlecode.aviator.runtime.type.AviatorObject;
import org.casbin.jcasbin.util.function.CustomFunction;

final class GuanjinSubjectRoleFunction extends CustomFunction {

    @Override
    public String getName() {
        return "g";
    }

    @Override
    public AviatorObject call(Map<String, Object> env, AviatorObject arg1, AviatorObject arg2) {
        Set<String> roles = rolesOf(arg1, env);
        String role = FunctionUtils.getStringValue(arg2, env);
        return AviatorBoolean.valueOf(role != null && roles.contains(role));
    }

    private Set<String> rolesOf(AviatorObject arg, Map<String, Object> env) {
        Object value = FunctionUtils.getJavaObject(arg, env);
        if (value instanceof GuanjinAuthzSubject) {
            return ((GuanjinAuthzSubject) value).roles;
        }
        if (value instanceof Map) {
            Object roles = ((Map<?, ?>) value).get("roles");
            if (roles instanceof Set) {
                @SuppressWarnings("unchecked")
                Set<String> cast = (Set<String>) roles;
                return cast;
            }
            if (roles instanceof Iterable) {
                Set<String> out = new java.util.LinkedHashSet<>();
                for (Object item : (Iterable<?>) roles) {
                    out.add(String.valueOf(item));
                }
                return out;
            }
        }
        return Collections.emptySet();
    }
}
