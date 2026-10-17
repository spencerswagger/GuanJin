package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinPrincipal;
import java.util.Collection;
import java.util.List;
import org.springframework.security.authentication.AbstractAuthenticationToken;
import org.springframework.security.core.GrantedAuthority;

public class GuanjinAuthentication extends AbstractAuthenticationToken {

    private final GuanjinPrincipal principal;

    public GuanjinAuthentication(GuanjinPrincipal principal, Collection<? extends GrantedAuthority> authorities) {
        super(authorities);
        this.principal = principal;
        setAuthenticated(true);
    }

    @Override
    public Object getCredentials() {
        return "";
    }

    @Override
    public Object getPrincipal() {
        return principal;
    }

    public GuanjinPrincipal getGuanjinPrincipal() {
        return principal;
    }

    public List<String> getRoles() {
        return principal.getRoles();
    }

    public String getAcr() {
        return principal.getAcr();
    }
}
