package com.guanjin.sdk.agent;

import java.util.List;

public final class GuanjinBinding {

    public String userId;
    public List<String> roles;

    public GuanjinBinding() {
    }

    public GuanjinBinding(String userId, List<String> roles) {
        this.userId = userId;
        this.roles = roles;
    }
}
