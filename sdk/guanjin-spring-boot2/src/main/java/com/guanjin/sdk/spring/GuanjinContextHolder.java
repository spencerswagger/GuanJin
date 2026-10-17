package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinPrincipal;

public final class GuanjinContextHolder {

    private static final ThreadLocal<GuanjinPrincipal> CONTEXT = new ThreadLocal<>();

    private GuanjinContextHolder() {
    }

    public static void set(GuanjinPrincipal principal) {
        CONTEXT.set(principal);
    }

    public static GuanjinPrincipal get() {
        return CONTEXT.get();
    }

    public static void clear() {
        CONTEXT.remove();
    }
}
