package com.guanjin.sdk.agent;

import com.guanjin.sdk.authz.GuanjinAuthzEngine;
import com.guanjin.sdk.authz.GuanjinAuthzModel;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class GuanjinPolicyReporter {

    public String clientId;
    public String sdkVersion;
    public long version;
    public String modelConfDigest;
    public List<String> permissions;
    public List<GuanjinRoleReport> roles;
    public List<GuanjinInterfaceMapEntry> interfaceMap;
    public GuanjinDefaultRoleReport defaultRole;
    public List<GuanjinLabelDef> labelDefs;

    public GuanjinPolicyReporter() {
    }

    public GuanjinPolicyReporter(String clientId,
                       String sdkVersion,
                       long version,
                       String modelConfDigest,
                       List<String> permissions,
                       List<GuanjinRoleReport> roles,
                       List<GuanjinInterfaceMapEntry> interfaceMap,
                       GuanjinDefaultRoleReport defaultRole,
                       List<GuanjinLabelDef> labelDefs) {
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

    public static GuanjinPolicyReporter from(GuanjinAuthzModel model,
                                   String clientId,
                                   String sdkVersion,
                                   long version,
                                   List<GuanjinInterfaceMapEntry> interfaceMap) {
        String modelText = model.modelConfText() != null ? model.modelConfText() : GuanjinAuthzEngine.MODEL_CONF;
        String digest = GuanjinTransport.sha256Hex(modelText.getBytes(StandardCharsets.UTF_8));
        Map<String, List<String>> rolePermissions = model.rolePermissions();

        List<GuanjinRoleReport> roles = new ArrayList<>();
        for (Map.Entry<String, List<String>> entry : rolePermissions.entrySet()) {
            roles.add(new GuanjinRoleReport(entry.getKey(), entry.getValue()));
        }

        GuanjinDefaultRoleReport defaultRole = null;
        if (model.defaultRole() != null) {
            defaultRole = new GuanjinDefaultRoleReport(model.defaultRole(),
                    rolePermissions.getOrDefault(model.defaultRole(), List.of()));
        }

        List<GuanjinLabelDef> labelDefs = new ArrayList<>();
        for (Map.Entry<String, Integer> entry : model.dataLabels().entrySet()) {
            labelDefs.add(new GuanjinLabelDef(entry.getKey(), entry.getValue()));
        }

        return new GuanjinPolicyReporter(clientId, sdkVersion, version, digest,
                model.permissionCatalog(), roles, interfaceMap, defaultRole, labelDefs);
    }
}
