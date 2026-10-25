package com.guanjin.sdk.authz;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

public final class GuanjinAuthzModel {

    private final Map<String, RoleDef> roles;
    private final Map<String, Set<String>> resourceGroups;
    private final Map<String, Integer> dataLabels;
    private final String defaultRole;
    private final String modelConfText;

    private GuanjinAuthzModel(Map<String, RoleDef> roles,
                       Map<String, Set<String>> resourceGroups,
                       Map<String, Integer> dataLabels,
                       String defaultRole,
                       String modelConfText) {
        this.roles = Collections.unmodifiableMap(roles);
        this.resourceGroups = Collections.unmodifiableMap(resourceGroups);
        this.dataLabels = Collections.unmodifiableMap(dataLabels);
        this.defaultRole = defaultRole;
        this.modelConfText = modelConfText;
    }

    public static Builder builder() {
        return new Builder();
    }

    public String defaultRole() {
        return defaultRole;
    }

    public String modelConfText() {
        return modelConfText;
    }

    public int clearance(String role) {
        RoleDef def = roles.get(role);
        return def == null ? 0 : def.clearance;
    }

    public int dataLabel(String resource) {
        Integer label = dataLabels.get(resource);
        return label == null ? 0 : label;
    }

    public Map<String, Set<String>> resourceGroups() {
        return resourceGroups;
    }

    public Map<String, Integer> dataLabels() {
        return dataLabels;
    }

    public List<String> roleNames() {
        return new ArrayList<>(roles.keySet());
    }

    public Map<String, Integer> clearances() {
        Map<String, Integer> out = new LinkedHashMap<>();
        for (Map.Entry<String, RoleDef> entry : roles.entrySet()) {
            out.put(entry.getKey(), entry.getValue().clearance);
        }
        return out;
    }

    public Map<String, List<String>> rolePermissions() {
        Map<String, List<String>> out = new LinkedHashMap<>();
        for (Map.Entry<String, RoleDef> entry : roles.entrySet()) {
            out.put(entry.getKey(), entry.getValue().permissionList());
        }
        return out;
    }

    public List<String> permissionCatalog() {
        Set<String> out = new LinkedHashSet<>();
        for (RoleDef def : roles.values()) {
            out.addAll(def.permissionList());
        }
        return new ArrayList<>(out);
    }

    public List<String[]> policies() {
        List<String[]> out = new ArrayList<>();
        Set<String> seen = new LinkedHashSet<>();
        for (Map.Entry<String, RoleDef> entry : roles.entrySet()) {
            for (Map.Entry<String, LinkedHashSet<String>> perm : entry.getValue().perms.entrySet()) {
                for (String action : perm.getValue()) {
                    addPolicy(out, seen, entry.getKey(), perm.getKey(), action);
                    Set<String> members = resourceGroups.get(perm.getKey());
                    if (members != null) {
                        for (String member : members) {
                            addPolicy(out, seen, entry.getKey(), member, action);
                        }
                    }
                }
            }
        }
        return out;
    }

    private static void addPolicy(List<String[]> out, Set<String> seen, String role, String resource, String action) {
        String key = role + "\u0000" + resource + "\u0000" + action;
        if (seen.add(key)) {
            out.add(new String[]{role, resource, action});
        }
    }

    static final class RoleDef {
        private final String name;
        private int clearance;
        private final Map<String, LinkedHashSet<String>> perms = new LinkedHashMap<>();

        private RoleDef(String name) {
            this.name = name;
        }

        private void add(String resource, String... actions) {
            LinkedHashSet<String> set = perms.computeIfAbsent(resource, key -> new LinkedHashSet<>());
            for (String action : actions) {
                if (action != null && !action.isEmpty()) {
                    set.add(action);
                }
            }
        }

        private List<String> permissionList() {
            List<String> out = new ArrayList<>();
            for (Map.Entry<String, LinkedHashSet<String>> entry : perms.entrySet()) {
                for (String action : entry.getValue()) {
                    out.add(entry.getKey() + ":" + action);
                }
            }
            return out;
        }
    }

    public static final class Builder {

        private final Map<String, RoleDef> roles = new LinkedHashMap<>();
        private final Map<String, Set<String>> resourceGroups = new LinkedHashMap<>();
        private final Map<String, Integer> dataLabels = new LinkedHashMap<>();
        private String currentRole;
        private String currentGroup;
        private String defaultRole;
        private String modelConfText;

        public Builder role(String name) {
            this.currentRole = name;
            this.currentGroup = null;
            roles.computeIfAbsent(name, RoleDef::new);
            return this;
        }

        public Builder can(String resource, String... actions) {
            if (currentRole == null) {
                throw new IllegalStateException("can() 之前必须先声明 role() / defaultRole()");
            }
            roles.get(currentRole).add(resource, actions);
            return this;
        }

        public Builder withClearance(int level) {
            if (currentRole == null) {
                throw new IllegalStateException("withClearance() 之前必须先声明 role()");
            }
            roles.get(currentRole).clearance = level;
            return this;
        }

        public Builder resourceGroup(String name) {
            this.currentGroup = name;
            this.currentRole = null;
            resourceGroups.computeIfAbsent(name, key -> new LinkedHashSet<>());
            return this;
        }

        public Builder contains(String... resources) {
            if (currentGroup == null) {
                throw new IllegalStateException("contains() 之前必须先声明 resourceGroup()");
            }
            Set<String> group = resourceGroups.get(currentGroup);
            for (String resource : resources) {
                if (resource != null && !resource.isEmpty()) {
                    group.add(resource);
                }
            }
            return this;
        }

        public Builder dataLabel(String resource, int level) {
            dataLabels.put(resource, level);
            return this;
        }

        public Builder defaultRole(String name) {
            this.defaultRole = name;
            this.currentRole = name;
            this.currentGroup = null;
            roles.computeIfAbsent(name, RoleDef::new);
            return this;
        }

        public Builder modelConf(String text) {
            this.modelConfText = text;
            return this;
        }

        public GuanjinAuthzModel build() {
            return new GuanjinAuthzModel(roles, resourceGroups, dataLabels, defaultRole, modelConfText);
        }
    }
}
