package com.guanjin.sdk.spring;

import com.guanjin.sdk.GuanjinAssertionVerifier;
import com.guanjin.sdk.GuanjinIdentityProvider;
import com.guanjin.sdk.GuanjinJwksCache;
import com.guanjin.sdk.agent.GuanjinAgentHmacClient;
import com.guanjin.sdk.agent.GuanjinHttpClientTransport;
import com.guanjin.sdk.agent.GuanjinInterfaceMapEntry;
import com.guanjin.sdk.agent.GuanjinPolicyReporter;
import com.guanjin.sdk.agent.GuanjinTransport;
import com.guanjin.sdk.authz.GuanjinAuthz;
import com.guanjin.sdk.authz.GuanjinAuthzEngine;
import com.guanjin.sdk.authz.GuanjinAuthzModel;
import com.guanjin.sdk.replay.GuanjinInMemoryJtiReplayStore;
import com.guanjin.sdk.replay.GuanjinJtiReplayStore;
import com.guanjin.sdk.replay.GuanjinRedisJtiReplayStore;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.ApplicationRunner;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.Ordered;
import org.springframework.core.annotation.AnnotatedElementUtils;
import org.springframework.core.annotation.Order;
import org.springframework.core.io.Resource;
import org.springframework.core.io.ResourceLoader;
import org.springframework.web.method.HandlerMethod;
import org.springframework.web.servlet.DispatcherServlet;
import org.springframework.web.servlet.config.annotation.InterceptorRegistry;
import org.springframework.web.servlet.config.annotation.WebMvcConfigurer;
import org.springframework.web.servlet.mvc.method.RequestMappingInfo;
import org.springframework.web.servlet.mvc.method.annotation.RequestMappingHandlerMapping;

@Configuration(proxyBeanMethods = false)
@EnableConfigurationProperties(GuanjinProperties.class)
@ConditionalOnProperty(prefix = "guanjin", name = "enabled", havingValue = "true", matchIfMissing = true)
public class GuanjinAutoConfiguration {

    private static final Logger log = LoggerFactory.getLogger(GuanjinAutoConfiguration.class);
    public static final String SDK_VERSION = "0.7.0";

    @Bean
    @ConditionalOnMissingBean
    public GuanjinJwksCache guanjinJwksCache(GuanjinProperties properties) {
        String uri = properties.effectiveJwksUri();
        if (uri == null || uri.isEmpty()) {
            throw new IllegalStateException("必须配置 guanjin.platform.base-url 或 guanjin.gateway.jwks-uri");
        }
        return new GuanjinJwksCache(uri, properties.effectiveJwksCacheTtl(),
                properties.getPlatform().getReadTimeout());
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinJtiReplayStore guanjinJtiReplayStore(GuanjinProperties properties) {
        if ("redis".equalsIgnoreCase(properties.effectiveReplayStore())) {
            GuanjinProperties.Redis redis = properties.getAuthz().getRedis();
            return GuanjinRedisJtiReplayStore.forServer(redis.getHost(), redis.getPort(),
                    redis.getPassword(), (int) redis.getTimeout().toMillis());
        }
        return new GuanjinInMemoryJtiReplayStore();
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinAssertionVerifier guanjinAssertionVerifier(GuanjinProperties properties,
                                                             GuanjinJwksCache jwksCache,
                                                             GuanjinJtiReplayStore replayStore) {
        String audience = properties.effectiveAudience();
        if (audience == null || audience.isEmpty()) {
            throw new IllegalStateException("必须配置 guanjin.client.id（作为 aud 期望值）");
        }
        return new GuanjinAssertionVerifier(jwksCache, properties.effectiveIssuer(), audience,
                properties.getGateway().getClockSkew().getSeconds(), replayStore);
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinIdentityProvider guanjinIdentityProvider() {
        return GuanjinContextHolder::get;
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinPublicPathMatcher guanjinPublicPathMatcher(GuanjinProperties properties) {
        return new GuanjinPublicPathMatcher(properties.getPublicPaths(),
                properties.isPublicPathsIncludeBuiltin());
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinAuthenticationConverter guanjinAuthenticationConverter(
            GuanjinProperties properties,
            ObjectProvider<GuanjinGrantedAuthoritiesResolver> resolverProvider) {
        return new GuanjinAuthenticationConverter(properties.getSecurity().getRolePrefix(),
                resolverProvider.getIfAvailable());
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinAssertionFilter guanjinAssertionFilter(GuanjinProperties properties,
                                                         GuanjinAssertionVerifier verifier,
                                                         GuanjinAuthenticationConverter converter,
                                                         GuanjinPublicPathMatcher publicPathMatcher) {
        return new GuanjinAssertionFilter(properties, verifier, converter, publicPathMatcher);
    }

    @Bean
    public FilterRegistrationBean<GuanjinAssertionFilter> guanjinAssertionFilterRegistration(
            GuanjinAssertionFilter filter) {
        FilterRegistrationBean<GuanjinAssertionFilter> registration = new FilterRegistrationBean<>(filter);
        registration.setOrder(Ordered.HIGHEST_PRECEDENCE + 100);
        registration.addUrlPatterns("/*");
        return registration;
    }

    @Bean
    @ConditionalOnMissingBean
    @ConditionalOnProperty(prefix = "guanjin.authz", name = "enabled", havingValue = "true", matchIfMissing = true)
    public GuanjinAuthzEngine guanjinAuthzEngine(GuanjinProperties properties,
                                                 ObjectProvider<GuanjinAuthzModel> modelProvider,
                                                 ResourceLoader resourceLoader) {
        GuanjinAuthzModel model = modelProvider.getIfAvailable();
        if (model == null) {
            model = GuanjinAuthzModel.builder()
                    .defaultRole(properties.getAuthz().getDefaultRole())
                    .build();
        }
        GuanjinAuthzEngine engine = new GuanjinAuthzEngine(model, GuanjinContextHolder::get,
                loadModelConf(properties, resourceLoader));
        GuanjinAuthz.bind(engine);
        return engine;
    }

    @Bean
    @ConditionalOnMissingBean
    @ConditionalOnClass(DispatcherServlet.class)
    @ConditionalOnProperty(prefix = "guanjin.authz", name = "enabled", havingValue = "true", matchIfMissing = true)
    public GuanjinPermissionInterceptor guanjinPermissionInterceptor(GuanjinAuthzEngine authzEngine) {
        return new GuanjinPermissionInterceptor(authzEngine);
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinTransport guanjinTransport(GuanjinProperties properties) {
        return new GuanjinHttpClientTransport(properties.getPlatform().getReadTimeout());
    }

    @Bean
    @ConditionalOnMissingBean
    public GuanjinAgentHmacClient guanjinAgentHmacClient(GuanjinProperties properties,
                                                         GuanjinTransport transport) {
        String secret = properties.effectiveAgentSecret();
        return new GuanjinAgentHmacClient(properties.effectiveAgentBaseUrl(),
                properties.effectiveAgentClientId(),
                secret == null ? new byte[0] : secret.getBytes(StandardCharsets.UTF_8),
                transport);
    }

    @Bean
    @Order(0)
    public ApplicationRunner guanjinStartupRunner(GuanjinProperties properties,
                                                  GuanjinPublicPathMatcher publicPathMatcher,
                                                  ObjectProvider<GuanjinAuthzModel> modelProvider,
                                                  ObjectProvider<GuanjinAgentHmacClient> agentClientProvider) {
        return args -> {
            if (properties.getStartup().isPrintPublicPaths()) {
                log.info("关津放行清单（deny-by-default）: {}", publicPathMatcher.patterns());
            }
            if (!properties.getAgent().getReport().isEnabled()
                    || !properties.getAgent().getReport().isOnStartup()) {
                return;
            }
            GuanjinAuthzModel model = modelProvider.getIfAvailable();
            GuanjinAgentHmacClient client = agentClientProvider.getIfAvailable();
            if (model == null || client == null) {
                return;
            }
            try {
                List<GuanjinInterfaceMapEntry> interfaceMap = new ArrayList<>();
                client.report(GuanjinPolicyReporter.from(model, properties.effectiveAgentClientId(),
                        SDK_VERSION, 1L, interfaceMap));
            } catch (RuntimeException e) {
                log.warn("关津策略上报失败（不阻塞业务）: {}", e.getMessage());
            }
        };
    }

    private static String loadModelConf(GuanjinProperties properties, ResourceLoader resourceLoader) {
        String location = properties.getAuthz().getModelConf();
        if (location == null || location.isEmpty()) {
            return null;
        }
        try {
            Resource resource = resourceLoader.getResource(location);
            if (!resource.exists()) {
                return null;
            }
            try (InputStream in = resource.getInputStream()) {
                return new String(in.readAllBytes(), StandardCharsets.UTF_8);
            }
        } catch (Exception e) {
            throw new IllegalStateException("加载 guanjin.authz.model-conf 失败: " + location, e);
        }
    }

    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(DispatcherServlet.class)
    @ConditionalOnProperty(prefix = "guanjin.authz", name = "enabled", havingValue = "true", matchIfMissing = true)
    static class GuanjinWebMvcConfiguration implements WebMvcConfigurer {

        private final GuanjinPermissionInterceptor interceptor;

        GuanjinWebMvcConfiguration(GuanjinPermissionInterceptor interceptor) {
            this.interceptor = interceptor;
        }

        @Override
        public void addInterceptors(InterceptorRegistry registry) {
            registry.addInterceptor(interceptor).addPathPatterns("/**");
        }

        @Bean
        @Order(Ordered.HIGHEST_PRECEDENCE)
        public ApplicationRunner guanjinPublicApiScanner(GuanjinPublicPathMatcher publicPathMatcher,
                                                         List<RequestMappingHandlerMapping> mappings) {
            return args -> {
                for (RequestMappingHandlerMapping mapping : mappings) {
                    for (Map.Entry<RequestMappingInfo, HandlerMethod> entry
                            : mapping.getHandlerMethods().entrySet()) {
                        HandlerMethod handlerMethod = entry.getValue();
                        if (AnnotatedElementUtils.hasAnnotation(handlerMethod.getMethod(), PublicApi.class)
                                || AnnotatedElementUtils.hasAnnotation(handlerMethod.getBeanType(), PublicApi.class)) {
                            publicPathMatcher.addPublicPaths(entry.getKey().getPatternValues());
                        }
                    }
                }
            };
        }
    }
}
