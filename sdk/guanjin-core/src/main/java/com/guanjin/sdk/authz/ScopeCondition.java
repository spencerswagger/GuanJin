package com.guanjin.sdk.authz;

public final class ScopeCondition {

    private final boolean unrestricted;
    private final String ownerUserId;

    private ScopeCondition(boolean unrestricted, String ownerUserId) {
        this.unrestricted = unrestricted;
        this.ownerUserId = ownerUserId;
    }

    public static ScopeCondition unrestricted() {
        return new ScopeCondition(true, null);
    }

    public static ScopeCondition onlyOwner(String ownerUserId) {
        return new ScopeCondition(false, ownerUserId);
    }

    public boolean isUnrestricted() {
        return unrestricted;
    }

    public String getOwnerUserId() {
        return ownerUserId;
    }

    public boolean isDenyAll() {
        return !unrestricted && (ownerUserId == null || ownerUserId.isEmpty());
    }

    @Override
    public String toString() {
        if (unrestricted) {
            return "ScopeCondition{unrestricted}";
        }
        if (isDenyAll()) {
            return "ScopeCondition{denyAll}";
        }
        return "ScopeCondition{ownerUserId=" + ownerUserId + '}';
    }
}
