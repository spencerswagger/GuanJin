package com.guanjin.core.authz;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * 业务方声明的授权模型（契约 GJ-CAP-13 {@code AuthzModel.builder()}，API 逐格冻结）。
 *
 * <pre>
 * AuthzModel.builder()
 *     .role("order_admin").can("orders", "read", "write")
 *     .role("order_viewer").can("orders", "read")
 *     .role("auditor").can("orders", "read")      // 名为 auditor 的角色自动获得行级豁免
 *     .defaultRole("user").can("profile", "read") // 默认角色：权限最小
 *     .role("manager").withClearance(3)           // 密级（GJ-CAP-16）
 *     .resourceGroup("order-res").contains("orders")
 *     .build();
 * </pre>
 *
 * <p><b>不内联硬编码策略</b>：这里产出的策略行会写进 Casbin，模型文本来自
 * {@code classpath:/guanjin/model.conf}（配置 #17）——两者都是"可审计的输入"，
 * 不是代码里 if-else。
 */
public final class AuthzModel {

    private final Map<String, Map<String, Set<String>>> rolePermissions;
    private final Map<String, Integer> roleClearances;
    private final Map<String, Set<String>> resourceGroups;
    private final String defaultRole;

    private AuthzModel(Map<String, Map<String, Set<String>>> rolePermissions,
                       Map<String, Integer> roleClearances,
                       Map<String, Set<String>> resourceGroups,
                       String defaultRole) {
        this.rolePermissions = rolePermissions;
        this.roleClearances = roleClearances;
        this.resourceGroups = resourceGroups;
        this.defaultRole = defaultRole;
    }

    public static Builder builder() {
        return new Builder();
    }

    /** 默认角色（GJ-CAP-18）：未绑角色时的兜底，权限最小。 */
    public String defaultRole() {
        return defaultRole;
    }

    /** 某角色的密级；未声明返回 {@code 0}（公开不占级）。 */
    public int clearanceOf(String role) {
        Integer v = roleClearances.get(role);
        return v == null ? 0 : v.intValue();
    }

    /** 生成 Casbin 策略行（{@code p, g, g2} 三类），供 Enforcer 装载。 */
    public List<String> toPolicyLines() {
        List<String> lines = new ArrayList<String>();
        for (Map.Entry<String, Map<String, Set<String>>> e : rolePermissions.entrySet()) {
            String role = e.getKey();
            lines.add("g, " + role + ", " + role); // 主体=角色自身的自链接（RBAC 底座）
            for (Map.Entry<String, Set<String>> perm : e.getValue().entrySet()) {
                for (String action : perm.getValue()) {
                    lines.add("p, " + role + ", " + perm.getKey() + ", " + action);
                }
            }
        }
        // 资源 → 自身的自链接（让 g2(r.obj, p.obj) 对未分组资源也成立）
        Set<String> resources = new LinkedHashSet<String>();
        for (Map<String, Set<String>> perms : rolePermissions.values()) {
            resources.addAll(perms.keySet());
        }
        for (String resource : resources) {
            lines.add("g2, " + resource + ", " + resource);
        }
        for (Map.Entry<String, Set<String>> group : resourceGroups.entrySet()) {
            lines.add("g2, " + group.getKey() + ", " + group.getKey());
            for (String resource : group.getValue()) {
                lines.add("g2, " + resource + ", " + group.getKey());
            }
        }
        return Collections.unmodifiableList(lines);
    }

    /** 声明用的 builder（API 名字按契约 §2.1 冻结：{@code role(name).can(...)}）。 */
    public static final class Builder {

        private final Map<String, Map<String, Set<String>>> rolePermissions =
                new LinkedHashMap<String, Map<String, Set<String>>>();
        private final Map<String, Integer> roleClearances = new LinkedHashMap<String, Integer>();
        private final Map<String, Set<String>> resourceGroups = new LinkedHashMap<String, Set<String>>();
        private String defaultRole;

        /** 声明一个角色，返回它的权限声明器。 */
        public RoleBuilder role(String name) {
            return new RoleBuilder(this, name);
        }

        /** 声明默认角色（GJ-CAP-18）：权限最小，不得"已登录即全部"。 */
        public RoleBuilder defaultRole(String name) {
            this.defaultRole = name;
            return new RoleBuilder(this, name);
        }

        /** 声明资源组（GJ-CAP-13 {@code resourceGroup(name).contains(resource)}）。 */
        public GroupBuilder resourceGroup(String name) {
            return new GroupBuilder(this, name);
        }

        public AuthzModel build() {
            if (defaultRole == null || defaultRole.trim().isEmpty()) {
                throw new IllegalStateException(
                        "AuthzModel 必须声明 defaultRole（GJ-CAP-18：权限最小兜底，不得\"已登录即全部\"）");
            }
            if (!rolePermissions.containsKey(defaultRole)) {
                throw new IllegalStateException(
                        "defaultRole(" + defaultRole + ") 必须先由 .defaultRole(...).can(...) 声明其权限");
            }
            return new AuthzModel(deepCopy(rolePermissions), new LinkedHashMap<String, Integer>(roleClearances),
                    deepCopyGroups(resourceGroups), defaultRole);
        }

        private static Map<String, Map<String, Set<String>>> deepCopy(
                Map<String, Map<String, Set<String>>> src) {
            Map<String, Map<String, Set<String>>> out = new LinkedHashMap<String, Map<String, Set<String>>>();
            for (Map.Entry<String, Map<String, Set<String>>> e : src.entrySet()) {
                Map<String, Set<String>> inner = new LinkedHashMap<String, Set<String>>();
                for (Map.Entry<String, Set<String>> p : e.getValue().entrySet()) {
                    inner.put(p.getKey(), new LinkedHashSet<String>(p.getValue()));
                }
                out.put(e.getKey(), inner);
            }
            return Collections.unmodifiableMap(out);
        }

        private static Map<String, Set<String>> deepCopyGroups(Map<String, Set<String>> src) {
            Map<String, Set<String>> out = new LinkedHashMap<String, Set<String>>();
            for (Map.Entry<String, Set<String>> e : src.entrySet()) {
                out.put(e.getKey(), Collections.unmodifiableSet(new LinkedHashSet<String>(e.getValue())));
            }
            return Collections.unmodifiableMap(out);
        }
    }

    /** 角色权限声明器：{@code can(resource, actions...)} / {@code withClearance(level)}。 */
    public static final class RoleBuilder {

        private final Builder owner;
        private final String role;

        RoleBuilder(Builder owner, String role) {
            this.owner = owner;
            this.role = role;
        }

        /** 声明该角色对某资源可执行的动作；{@code "*"} 表示该资源全部动作（须显式写，不是默认）。 */
        public RoleBuilder can(String resource, String... actions) {
            if (resource == null || resource.trim().isEmpty()) {
                throw new IllegalArgumentException("resource 不能为空");
            }
            if (actions == null || actions.length == 0) {
                throw new IllegalArgumentException("必须显式列出 action（不做\"空即全部\"的推断）");
            }
            Map<String, Set<String>> perms = owner.rolePermissions.get(role);
            if (perms == null) {
                perms = new LinkedHashMap<String, Set<String>>();
                owner.rolePermissions.put(role, perms);
            }
            Set<String> acts = perms.get(resource);
            if (acts == null) {
                acts = new LinkedHashSet<String>();
                perms.put(resource, acts);
            }
            for (String a : actions) {
                if (a == null || a.trim().isEmpty()) {
                    throw new IllegalArgumentException("action 不能为空");
                }
                acts.add(a.trim());
            }
            return this;
        }

        /** 声明该角色的密级（1/2/3；公开不占级 = 0）。 */
        public RoleBuilder withClearance(int level) {
            if (level < 1 || level > 3) {
                throw new IllegalArgumentException("密级取值只允许 1/2/3（公开不占级）");
            }
            owner.roleClearances.put(role, Integer.valueOf(level));
            return this;
        }

        public RoleBuilder role(String name) {
            return owner.role(name);
        }

        public RoleBuilder defaultRole(String name) {
            return owner.defaultRole(name);
        }

        public GroupBuilder resourceGroup(String name) {
            return owner.resourceGroup(name);
        }

        public AuthzModel build() {
            return owner.build();
        }
    }

    /** 资源组声明器：{@code resourceGroup(name).contains(resource)}。 */
    public static final class GroupBuilder {

        private final Builder owner;
        private final String group;

        GroupBuilder(Builder owner, String group) {
            this.owner = owner;
            this.group = group;
        }

        public GroupBuilder contains(String resource) {
            if (resource == null || resource.trim().isEmpty()) {
                throw new IllegalArgumentException("resource 不能为空");
            }
            Set<String> set = owner.resourceGroups.get(group);
            if (set == null) {
                set = new LinkedHashSet<String>();
                owner.resourceGroups.put(group, set);
            }
            set.add(resource.trim());
            return this;
        }

        public RoleBuilder role(String name) {
            return owner.role(name);
        }

        public AuthzModel build() {
            return owner.build();
        }
    }
}
