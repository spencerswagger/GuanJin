package com.guanjin.sdk.agent;

import java.util.List;

public final class GuanjinPoliciesResponse {

    public long version;
    public List<GuanjinBinding> bindings;
    public String issuedAt;

    public GuanjinPoliciesResponse() {
    }

    public GuanjinPoliciesResponse(long version, List<GuanjinBinding> bindings, String issuedAt) {
        this.version = version;
        this.bindings = bindings;
        this.issuedAt = issuedAt;
    }
}
