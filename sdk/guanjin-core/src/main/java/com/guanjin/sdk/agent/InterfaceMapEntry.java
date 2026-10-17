package com.guanjin.sdk.agent;

public final class InterfaceMapEntry {

    public String path;
    public String method;
    public String resource;
    public String action;

    public InterfaceMapEntry() {
    }

    public InterfaceMapEntry(String path, String method, String resource, String action) {
        this.path = path;
        this.method = method;
        this.resource = resource;
        this.action = action;
    }
}
