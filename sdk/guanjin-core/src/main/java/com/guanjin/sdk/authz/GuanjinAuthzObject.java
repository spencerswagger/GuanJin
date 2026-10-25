package com.guanjin.sdk.authz;

public final class GuanjinAuthzObject {

    public final String name;
    public final String owner;
    public final int label;

    public GuanjinAuthzObject(String name, String owner, int label) {
        this.name = name;
        this.owner = owner == null ? "" : owner;
        this.label = label;
    }

    public String getName() {
        return name;
    }

    public String getOwner() {
        return owner;
    }

    public int getLabel() {
        return label;
    }
}
