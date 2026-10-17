package com.guanjin.sdk.agent;

import java.util.List;

public final class PoliciesResponse {

    public long version;
    public List<Binding> bindings;
    public String issuedAt;

    public PoliciesResponse() {
    }

    public PoliciesResponse(long version, List<Binding> bindings, String issuedAt) {
        this.version = version;
        this.bindings = bindings;
        this.issuedAt = issuedAt;
    }
}
