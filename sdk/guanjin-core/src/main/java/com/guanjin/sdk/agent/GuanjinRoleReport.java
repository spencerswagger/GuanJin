package com.guanjin.sdk.agent;

import java.util.List;

public final class GuanjinRoleReport {

    public String role;
    public List<String> permissions;

    public GuanjinRoleReport() {
    }

    public GuanjinRoleReport(String role, List<String> permissions) {
        this.role = role;
        this.permissions = permissions;
    }
}
