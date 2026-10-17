package com.guanjin.sdk.authz;

import java.util.Collection;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.Set;

public final class AuthzSubject {

    public final String name;
    public final int clearance;
    public final Set<String> roles;

    public AuthzSubject(String name, int clearance, Collection<String> roles) {
        this.name = name;
        this.clearance = clearance;
        this.roles = roles == null ? Collections.emptySet() : Collections.unmodifiableSet(new LinkedHashSet<>(roles));
    }

    public String getName() {
        return name;
    }

    public int getClearance() {
        return clearance;
    }

    public Set<String> getRoles() {
        return roles;
    }
}
