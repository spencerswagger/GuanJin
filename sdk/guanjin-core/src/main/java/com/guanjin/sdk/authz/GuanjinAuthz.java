package com.guanjin.sdk.authz;

import com.guanjin.sdk.GuanjinAccessDeniedException;
import com.guanjin.sdk.GuanjinDependencyException;

public final class GuanjinAuthz {

    private static volatile GuanjinAuthzEngine engine;

    private GuanjinAuthz() {
    }

    public static void bind(GuanjinAuthzEngine authzEngine) {
        engine = authzEngine;
    }

    public static GuanjinAuthzEngine engine() {
        GuanjinAuthzEngine current = engine;
        if (current == null) {
            throw new GuanjinDependencyException("GuanjinAuthz 尚未初始化");
        }
        return current;
    }

    public static boolean check(String resource, String action) {
        return engine().check(resource, action);
    }

    public static boolean require(String resource, String action) {
        if (!check(resource, action)) {
            throw new GuanjinAccessDeniedException("无权访问 " + resource + ":" + action);
        }
        return true;
    }

    public static void checkRow(String ownerUserId) {
        if (!engine().checkRow(ownerUserId)) {
            throw new GuanjinAccessDeniedException("行级权限不足");
        }
    }

    public static void checkOwner(String ownerUserId) {
        checkRow(ownerUserId);
    }

    public static void checkLabel(int label) {
        if (!engine().checkLabel(label)) {
            throw new GuanjinAccessDeniedException("密级不足");
        }
    }

    public static GuanjinScopeCondition scope(String resource) {
        return engine().scope(resource);
    }

    public static boolean hasRole(String role) {
        return engine().hasRole(role);
    }
}
