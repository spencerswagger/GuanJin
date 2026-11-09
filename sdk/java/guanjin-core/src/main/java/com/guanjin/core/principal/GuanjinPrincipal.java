package com.guanjin.core.principal;

import com.guanjin.core.assertion.Assertion;

import java.util.Collections;
import java.util.List;

/** 业务侧可见的身份（GJ-CAP-09 {@code ReadIdentity}）。 */
public final class GuanjinPrincipal {

    private final String subject;
    private final String username;
    private final List<String> roles;
    private final String acr;
    private final List<String> amr;
    private final String clientId;
    private final String jti;

    public GuanjinPrincipal(String subject, String username, List<String> roles, String acr,
                            List<String> amr, String clientId, String jti) {
        this.subject = subject;
        this.username = username;
        this.roles = roles == null ? Collections.<String>emptyList() : Collections.unmodifiableList(roles);
        this.acr = acr;
        this.amr = amr == null ? Collections.<String>emptyList() : Collections.unmodifiableList(amr);
        this.clientId = clientId;
        this.jti = jti;
    }

    /** 从已验签断言构造（GJ-CAP-09）。 */
    public static GuanjinPrincipal from(Assertion a) {
        return new GuanjinPrincipal(a.subject(), a.username(), a.roles(), a.acr(), a.amr(),
                a.clientId(), a.jti());
    }

    public String subject() {
        return subject;
    }

    public String username() {
        return username;
    }

    public List<String> roles() {
        return roles;
    }

    public String acr() {
        return acr;
    }

    public List<String> amr() {
        return amr;
    }

    public String clientId() {
        return clientId;
    }

    public String jti() {
        return jti;
    }

    @Override
    public String toString() {
        return "GuanjinPrincipal{sub=" + subject + ", roles=" + roles + ", acr=" + acr + "}";
    }
}
