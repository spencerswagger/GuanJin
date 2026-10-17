package com.guanjin.sdk.agent;

import com.guanjin.sdk.authz.AuthzEngine;
import com.guanjin.sdk.authz.AuthzModel;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class AgentReport {

    public String clientId;
    public String sdkVersion;
    public long version;
    public String modelConfDigest;
    public List<String> permissions;
    public List<RoleReport> roles;
    public List<InterfaceMapEntry> interfaceMap;
    public DefaultRoleReport defaultRole;
    public List<LabelDef> labelDefs;

    public AgentReport() {
    }

    public AgentReport(String clientId,
                       String sdkVersion,
                       long version,
                       String modelConfDigest,
                       List<String> permissions,
                       List<RoleReport> roles,
                       List<InterfaceMapEntry> interfaceMap,
                       DefaultRoleReport defaultRole,
                       List<LabelDef> labelDefs) {
        this.clientId = clientId;
        this.sdkVersion = sdkVersion;
        this.version = version;
        this.modelConfDigest = modelConfDigest;
        this.permissions = permissions;
        this.roles = roles;
        this.interfaceMap = interfaceMap;
        this.defaultRole = defaultRole;
        this.labelDefs = labelDefs;
    }

    public static AgentReport from(AuthzModel model,
                                   String clientId,
                                   String sdkVersion,
                                   long version,
                                   List<InterfaceMapEntry> interfaceMap) {
        String modelText = model.modelConfText() != null ? model.modelConfText() : AuthzEngine.MODEL_CONF;
        String digest = Transport.sha256Hex(modelText.getBytes(StandardCharsets.UTF_8));
        Map<String, List<String>> rolePermissions = model.rolePermissions();

        List<RoleReport> roles = new ArrayList<>();
        for (Map.Entry<String, List<String>> entry : rolePermissions.entrySet()) {
            roles.add(new RoleReport(entry.getKey(), entry.getValue()));
        }

        DefaultRoleReport defaultRole = null;
        if (model.defaultRole() != null) {
            defaultRole = new DefaultRoleReport(model.defaultRole(),
                    rolePermissions.getOrDefault(model.defaultRole(), List.of()));
        }

        List<LabelDef> labelDefs = new ArrayList<>();
        for (Map.Entry<String, Integer> entry : model.dataLabels().entrySet()) {
            labelDefs.add(new LabelDef(entry.getKey(), entry.getValue()));
        }

        return new AgentReport(clientId, sdkVersion, version, digest,
                model.permissionCatalog(), roles, interfaceMap, defaultRole, labelDefs);
    }
}
