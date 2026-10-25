package com.guanjin.sdk.agent;

import java.util.List;

public final class GuanjinDefaultRoleReport {

    public String role;
    public List<String> permissions;

    public GuanjinDefaultRoleReport() {
    }

    public GuanjinDefaultRoleReport(String role, List<String> permissions) {
        this.role = role;
        this.permissions = permissions;
    }
}
