package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinIdentity;
import java.util.Collection;
import org.springframework.security.core.GrantedAuthority;

@FunctionalInterface
public interface GuanjinGrantedAuthoritiesResolver {

    Collection<GrantedAuthority> resolve(GuanjinIdentity principal);
}
