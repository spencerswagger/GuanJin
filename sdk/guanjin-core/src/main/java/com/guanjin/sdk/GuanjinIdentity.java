package com.guanjin.sdk;

import java.util.Collections;
import java.util.List;
import java.util.Objects;

public final class GuanjinIdentity {

    private final String sub;
    private final String aud;
    private final String cid;
    private final String uname;
    private final List<String> roles;
    private final String acr;
    private final List<String> amr;
    private final long iat;
    private final long exp;
    private final String jti;

    public GuanjinIdentity(String sub,
                            String aud,
                            String cid,
                            String uname,
                            List<String> roles,
                            String acr,
                            List<String> amr,
                            long iat,
                            long exp,
                            String jti) {
        this.sub = sub;
        this.aud = aud;
        this.cid = cid;
        this.uname = uname;
        this.roles = roles == null ? Collections.emptyList() : Collections.unmodifiableList(roles);
        this.acr = acr;
        this.amr = amr == null ? Collections.emptyList() : Collections.unmodifiableList(amr);
        this.iat = iat;
        this.exp = exp;
        this.jti = jti;
    }

    public String getSub() {
        return sub;
    }

    public String getAud() {
        return aud;
    }

    public String getCid() {
        return cid;
    }

    public String getUname() {
        return uname;
    }

    public List<String> getRoles() {
        return roles;
    }

    public String getAcr() {
        return acr;
    }

    public List<String> getAmr() {
        return amr;
    }

    public long getIat() {
        return iat;
    }

    public long getExp() {
        return exp;
    }

    public String getJti() {
        return jti;
    }

    public boolean hasRole(String role) {
        return roles.contains(role);
    }

    @Override
    public String toString() {
        return "GuanjinIdentity{sub=" + sub + ", aud=" + aud + ", cid=" + cid + ", uname=" + uname
                + ", roles=" + roles + ", acr=" + acr + ", amr=" + amr + ", iat=" + iat
                + ", exp=" + exp + ", jti=" + jti + '}';
    }

    @Override
    public boolean equals(Object o) {
        if (this == o) {
            return true;
        }
        if (!(o instanceof GuanjinIdentity)) {
            return false;
        }
        GuanjinIdentity that = (GuanjinIdentity) o;
        return iat == that.iat && exp == that.exp && Objects.equals(sub, that.sub)
                && Objects.equals(aud, that.aud) && Objects.equals(cid, that.cid)
                && Objects.equals(uname, that.uname) && Objects.equals(roles, that.roles)
                && Objects.equals(acr, that.acr) && Objects.equals(amr, that.amr)
                && Objects.equals(jti, that.jti);
    }

    @Override
    public int hashCode() {
        return Objects.hash(sub, aud, cid, uname, roles, acr, amr, iat, exp, jti);
    }
}
