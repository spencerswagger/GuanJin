package com.guanjin.sdk.spring;

import com.guanjin.sdk.authz.GuanjinAuthzModel;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class TestAuthzConfiguration {

    @Bean
    public GuanjinAuthzModel authzModel() {
        return GuanjinAuthzModel.builder()
                .role("ADMIN").withClearance(3).can("orders", "read", "write")
                .role("order_viewer").can("orders", "read")
                .role("auditor").can("orders", "read")
                .defaultRole("user").can("profile", "read")
                .dataLabel("orders", 1)
                .role("secret_reader").withClearance(3).can("orders", "read")
                .build();
    }
}
