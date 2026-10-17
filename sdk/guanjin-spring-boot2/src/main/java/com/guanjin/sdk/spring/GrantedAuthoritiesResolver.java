package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinPrincipal;
import java.util.Collection;
import org.springframework.security.core.GrantedAuthority;

@FunctionalInterface
public interface GrantedAuthoritiesResolver {

    Collection<GrantedAuthority> resolve(GuanjinPrincipal principal);
}
