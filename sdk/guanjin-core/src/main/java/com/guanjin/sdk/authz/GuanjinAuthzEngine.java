package com.guanjin.sdk.authz;

import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinIdentity;
import com.guanjin.sdk.GuanjinIdentityProvider;
import java.util.LinkedHashSet;
import java.util.Set;
import org.casbin.jcasbin.main.Enforcer;
import org.casbin.jcasbin.model.Model;

public final class GuanjinAuthzEngine {

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

    private final GuanjinAuthzModel model;
    private final GuanjinIdentityProvider principalProvider;
    private final Enforcer enforcer;

    public GuanjinAuthzEngine(GuanjinAuthzModel model, GuanjinIdentityProvider principalProvider) {
        this(model, principalProvider, null);
    }

    public GuanjinAuthzEngine(GuanjinAuthzModel model, GuanjinIdentityProvider principalProvider, String modelTextOverride) {
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
        this.enforcer.addFunction("g", new GuanjinSubjectRoleFunction());
        loadPolicies();
    }

    private static String resolveModelText(GuanjinAuthzModel model, String modelTextOverride) {
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

    public GuanjinAuthzModel model() {
        return model;
    }

    public Enforcer enforcer() {
        return enforcer;
    }

    public GuanjinIdentity principal() {
        return principalProvider.get();
    }

    public boolean check(String resource, String action) {
        GuanjinIdentity principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        GuanjinAuthzSubject subject = subject(principal);
        GuanjinAuthzObject object = new GuanjinAuthzObject(resource, "", model.dataLabel(resource));
        return enforcer.enforce(subject, object, action);
    }

    public boolean checkRow(String ownerUserId) {
        GuanjinIdentity principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        if (ownerUserId == null || ownerUserId.isEmpty()) {
            return true;
        }
        return ownerUserId.equals(principal.getSub()) || principal.hasRole(AUDITOR_ROLE);
    }

    public boolean checkOwner(String ownerUserId) {
        return checkRow(ownerUserId);
    }

    public boolean checkLabel(int label) {
        GuanjinIdentity principal = principalProvider.get();
        if (principal == null) {
            return false;
        }
        if (label <= 0) {
            return true;
        }
        return clearance(principal) >= label;
    }

    public GuanjinScopeCondition scope(String resource) {
        GuanjinIdentity principal = principalProvider.get();
        if (principal == null) {
            return GuanjinScopeCondition.onlyOwner(null);
        }
        if (principal.hasRole(AUDITOR_ROLE)) {
            return GuanjinScopeCondition.unrestricted();
        }
        if (check(resource, "write")) {
            return GuanjinScopeCondition.unrestricted();
        }
        return GuanjinScopeCondition.onlyOwner(principal.getSub());
    }

    public boolean hasRole(String role) {
        GuanjinIdentity principal = principalProvider.get();
        return principal != null && principal.hasRole(role);
    }

    public int clearance(GuanjinIdentity principal) {
        int clearance = 0;
        for (String role : principal.getRoles()) {
            clearance = Math.max(clearance, model.clearance(role));
        }
        if (model.defaultRole() != null) {
            clearance = Math.max(clearance, model.clearance(model.defaultRole()));
        }
        return clearance;
    }

    private GuanjinAuthzSubject subject(GuanjinIdentity principal) {
        Set<String> roles = new LinkedHashSet<>(principal.getRoles());
        if (model.defaultRole() != null) {
            roles.add(model.defaultRole());
        }
        return new GuanjinAuthzSubject(principal.getSub(), clearance(principal), roles);
    }
}
