package com.guanjin.sdk.agent;

import java.util.List;

public final class RoleReport {

    public String role;
    public List<String> permissions;

    public RoleReport() {
    }

    public RoleReport(String role, List<String> permissions) {
        this.role = role;
        this.permissions = permissions;
    }
}
