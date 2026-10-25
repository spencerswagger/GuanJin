package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinJwksCache;
import java.time.Duration;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;

@TestConfiguration
public class TestJwksConfiguration {

    @Bean
    public GuanjinJwksCache guanjinJwksCache() {
        String jwksJson = VectorFixtures.read("jwks.json");
        return new GuanjinJwksCache("https://platform.example.com/api/v1/jwks",
                Duration.ofMinutes(10), uri -> jwksJson);
    }
}
