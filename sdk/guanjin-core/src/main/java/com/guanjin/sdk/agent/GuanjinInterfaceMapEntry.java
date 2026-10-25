package com.guanjin.sdk.agent;

public final class GuanjinInterfaceMapEntry {

    public String path;
    public String method;
    public String resource;
    public String action;

    public GuanjinInterfaceMapEntry() {
    }

    public GuanjinInterfaceMapEntry(String path, String method, String resource, String action) {
        this.path = path;
        this.method = method;
        this.resource = resource;
        this.action = action;
    }
}
