package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinIdentity;

public final class GuanjinContextHolder {

    private static final ThreadLocal<GuanjinIdentity> CONTEXT = new ThreadLocal<>();

    private GuanjinContextHolder() {
    }

    public static void set(GuanjinIdentity principal) {
        CONTEXT.set(principal);
    }

    public static GuanjinIdentity get() {
        return CONTEXT.get();
    }

    public static void clear() {
        CONTEXT.remove();
    }
}
