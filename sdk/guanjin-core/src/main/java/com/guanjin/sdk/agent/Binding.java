package com.guanjin.sdk.agent;

import java.util.List;

public final class Binding {

    public String userId;
    public List<String> roles;

    public Binding() {
    }

    public Binding(String userId, List<String> roles) {
        this.userId = userId;
        this.roles = roles;
    }
}
