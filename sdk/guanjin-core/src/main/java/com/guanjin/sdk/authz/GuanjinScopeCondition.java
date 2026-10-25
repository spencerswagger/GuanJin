package com.guanjin.sdk.authz;

public final class GuanjinScopeCondition {

    private final boolean unrestricted;
    private final String ownerUserId;

    private GuanjinScopeCondition(boolean unrestricted, String ownerUserId) {
        this.unrestricted = unrestricted;
        this.ownerUserId = ownerUserId;
    }

    public static GuanjinScopeCondition unrestricted() {
        return new GuanjinScopeCondition(true, null);
    }

    public static GuanjinScopeCondition onlyOwner(String ownerUserId) {
        return new GuanjinScopeCondition(false, ownerUserId);
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
            return "GuanjinScopeCondition{unrestricted}";
        }
        if (isDenyAll()) {
            return "GuanjinScopeCondition{denyAll}";
        }
        return "GuanjinScopeCondition{ownerUserId=" + ownerUserId + '}';
    }
}
