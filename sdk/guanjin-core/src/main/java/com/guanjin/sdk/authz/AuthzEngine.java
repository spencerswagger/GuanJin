package com.guanjin.sdk.authz;

import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinPrincipal;
import com.guanjin.sdk.PrincipalProvider;
import java.util.LinkedHashSet;
import java.util.Set;
import org.casbin.jcasbin.main.Enforcer;
import org.casbin.jcasbin.model.Model;

public final class AuthzEngine {

    public static final String AUDITOR_ROLE = "auditor";

    public static final String MODEL_CONF = String.join("\n",
            "[request_definition]",
            "r = sub, obj, act",
            "",
            "[policy_definition]",
            "p = sub, obj, act",
            "",
            "[role_definition]",
            "g = _, _",
            "",
            "[policy_effect]",
            "e = some(where (p.eft == allow))",
            "",
            "[matchers]",
            "m = g(r.sub, p.sub) && r.obj.name == p.obj && r.act == p.act"
                    + " && (r.obj.owner == \"\" || r.obj.owner == r.sub.name || g(r.sub, \"" + AUDITOR_ROLE + "\"))"
                    + " && (r.obj.label == 0 || r.sub.clearance >= r.obj.label)");

    private final AuthzModel model;
    private final PrincipalProvider principalProvider;
    private final Enforcer enforcer;

    public AuthzEngine(AuthzModel model, PrincipalProvider principalProvider) {
        this(model, principalProvider, null);
    }

    public AuthzEngine(AuthzModel model, PrincipalProvider principalProvider, String modelTextOverride) {
        this.model = model;
        this.principalProvider = principalProvider;
        String text = resolveModelText(model, modelTextOverride);
        Model casbinModel;
        try {
            casbinModel = Model.newModelFromString(text);
        } catch (RuntimeException e) {
            throw new GuanjinDependencyException("加载 model.conf 失败: " + e.getMessage(), e);
        }
        this.enforcer = new Enforcer(casbinModel);
        this.enforcer.addFunction("g", new SubjectRoleFunction());
        loadPolicies();
    }

    private static String resolveModelText(AuthzModel model, String modelTextOverride) {
        if (modelTextOverride != null && !modelTextOverride.isBlank()) {
            return modelTextOverride;
        }
        if (model.modelConfText() != null && !model.modelConfText().isBlank()) {
            return model.modelConfText();
        }
        return MODEL_CONF;
    }

    private void loadPolicies() {
        for (String[] policy : model.policies()) {
            enforcer.addPolicy(policy[0], policy[1], policy[2]);
        }
    }

    public AuthzModel model() {
        return model;
    }

    public Enforcer enforcer() {
        return enforcer;
    }

    public GuanjinPrincipal principal() {
        return principalProvider.get();
    }

    public boolean check(String resource, String action) {
        GuanjinPrincipal principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        AuthzSubject subject = subject(principal);
        AuthzObject object = new AuthzObject(resource, "", model.dataLabel(resource));
        return enforcer.enforce(subject, object, action);
    }

    public boolean checkRow(String ownerUserId) {
        GuanjinPrincipal principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        if (ownerUserId == null || ownerUserId.isEmpty()) {
            return true;
        }
        return ownerUserId.equals(principal.getSub()) || principal.hasRole(AUDITOR_ROLE);
    }

    public boolean checkLabel(int label) {
        GuanjinPrincipal principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        if (label <= 0) {
            return true;
        }
        return clearance(principal) >= label;
    }

    public ScopeCondition scope(String resource) {
        GuanjinPrincipal principal = principalProvider.get();
        if (principal == null) {
            return ScopeCondition.onlyOwner(null);
        }
        if (principal.hasRole(AUDITOR_ROLE)) {
            return ScopeCondition.unrestricted();
        }
        if (check(resource, "write")) {
            return ScopeCondition.unrestricted();
        }
        return ScopeCondition.onlyOwner(principal.getSub());
    }

    public boolean hasRole(String role) {
        GuanjinPrincipal principal = principalProvider.get();
        return principal != null && principal.hasRole(role);
    }

    public int clearance(GuanjinPrincipal principal) {
        int clearance = 0;
        for (String role : principal.getRoles()) {
            clearance = Math.max(clearance, model.clearance(role));
        }
        if (model.defaultRole() != null) {
            clearance = Math.max(clearance, model.clearance(model.defaultRole()));
        }
        return clearance;
    }

    private AuthzSubject subject(GuanjinPrincipal principal) {
        Set<String> roles = new LinkedHashSet<>(principal.getRoles());
        if (model.defaultRole() != null) {
            roles.add(model.defaultRole());
        }
        return new AuthzSubject(principal.getSub(), clearance(principal), roles);
    }
}
