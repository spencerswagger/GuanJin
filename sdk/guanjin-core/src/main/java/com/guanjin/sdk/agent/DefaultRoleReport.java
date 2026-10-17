package com.guanjin.sdk.agent;

import java.util.List;

public final class DefaultRoleReport {

    public String role;
    public List<String> permissions;

    public DefaultRoleReport() {
    }

    public DefaultRoleReport(String role, List<String> permissions) {
        this.role = role;
        this.permissions = permissions;
    }
}
