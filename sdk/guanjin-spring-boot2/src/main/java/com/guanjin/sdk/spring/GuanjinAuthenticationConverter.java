package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinIdentity;
import java.util.ArrayList;
import java.util.List;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;

public class GuanjinAuthenticationConverter {

    private final String rolePrefix;
    private final GuanjinGrantedAuthoritiesResolver resolver;

    public GuanjinAuthenticationConverter(String rolePrefix, GuanjinGrantedAuthoritiesResolver resolver) {
        this.rolePrefix = rolePrefix == null ? "" : rolePrefix;
        this.resolver = resolver;
    }

    public Authentication toAuthentication(GuanjinIdentity principal) {
        List<GrantedAuthority> authorities = new ArrayList<>();
        for (String role : principal.getRoles()) {
            authorities.add(new SimpleGrantedAuthority(rolePrefix + role));
        }
        if (resolver != null) {
            authorities.addAll(resolver.resolve(principal));
        }
        return new GuanjinAuthentication(principal, authorities);
    }
}
