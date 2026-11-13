package com.guanjin.spring.boot2;

import com.guanjin.core.agent.PolicyPuller;
import com.guanjin.core.agent.PullRequestSigner;
import com.guanjin.core.assertion.AssertionVerifier;
import com.guanjin.core.assertion.Sm2AssertionVerifier;
import com.guanjin.core.authz.Authz;
import com.guanjin.core.authz.AuthzModel;
import com.guanjin.core.config.GuanjinConfigException;
import com.guanjin.core.config.GuanjinConfigKeys;
import com.guanjin.core.config.GuanjinEnvironment;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.UrlConnectionHttpFetcher;
import com.guanjin.core.jwks.JwksClient;
import com.guanjin.core.publicpath.PathMatcher;
import com.guanjin.core.replay.InMemoryReplayStore;
import com.guanjin.core.replay.ReplayGuard;
import com.guanjin.core.replay.ReplayStore;
import com.guanjin.core.startup.GuanjinStartupChecker;
import com.guanjin.core.startup.StartupReport;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.Environment;

import java.net.URI;
import java.util.Arrays;

/**
 * Spring Boot 2.7（javax）自动配置。
 *
 * <p>注册方式按 2.x 惯例走 {@code META-INF/spring.factories}（不是 3.x 的
 * {@code AutoConfiguration.imports}）。
 *
 * <p><b>没有"未配置就先跑起来"的分支</b>（AC-SDK-04）：本自动配置只要在 classpath 上就生效，
 * 缺 {@code platform.base-url} / {@code client.id} / {@code client.hmac-secret} 即启动失败。
 * 想不接入只能 {@code guanjin.enabled=false}，而它**只允许在 {@code local|test}**（C-1 / AC-SDK-02）。
 */
@Configuration(proxyBeanMethods = false)
@EnableConfigurationProperties(GuanjinProperties.class)
public class GuanjinAutoConfiguration {

    private static final Logger LOG = LoggerFactory.getLogger("guanjin.sdk");

    /** 出站 HTTP（GJ-CAP-03/21 共用；宿主可覆盖，如换 OkHttp）。 */
    @Bean
    @ConditionalOnMissingBean(HttpFetcher.class)
    public HttpFetcher guanjinHttpFetcher(GuanjinProperties properties) {
        return new UrlConnectionHttpFetcher(properties.getPlatform().getConnectTimeout(),
                properties.getPlatform().getReadTimeout());
    }

    /** JWKS 客户端（GJ-CAP-02/03/04）。 */
    @Bean
    @ConditionalOnMissingBean(JwksClient.class)
    public JwksClient guanjinJwksClient(GuanjinProperties properties, HttpFetcher fetcher) {
        String jwksUri = properties.resolvedJwksUri();
        if (jwksUri == null || jwksUri.trim().isEmpty()) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "为空（必填、无内置默认值；JWKS 地址由它派生，缺失即启动失败；AC-SDK-04）");
        }
        return new JwksClient(URI.create(jwksUri), fetcher,
                properties.getGateway().getJwksCacheTtl(),
                properties.getGateway().isJwksUnknownKidForceRefresh());
    }

    /**
     * 断言验签（GJ-CAP-01）：**默认就是 SM2 真验签**（{@code alg=SM2}（D29），+ {@code kid} 选键 + 不协商算法）。
     *
     * <p>宿主可覆盖（{@code @ConditionalOnMissingBean}），但**不能关掉**：没有验签器的形态不存在，
     * 因为这里总会提供一个。
     */
    @Bean
    @ConditionalOnMissingBean(AssertionVerifier.class)
    public AssertionVerifier guanjinSm2AssertionVerifier(JwksClient jwksClient) {
        return new Sm2AssertionVerifier(jwksClient);
    }

    /** 放行清单（GJ-CAP-19）。 */
    @Bean
    @ConditionalOnMissingBean(PathMatcher.class)
    public PathMatcher guanjinPathMatcher(GuanjinProperties properties) {
        return new PathMatcher(properties.getPublicPaths(), properties.isPublicPathsIncludeBuiltin());
    }

    /**
     * 重放判定（GJ-CAP-07）。{@code replay-store=redis} 而容器里没有可用的存储 bean 时
     * **启动失败**，不静默降级到 memory（那是索引 §2 禁止的降级放行）。
     */
    @Bean
    @ConditionalOnMissingBean(ReplayGuard.class)
    @ConditionalOnProperty(prefix = "guanjin", name = "enabled", havingValue = "true", matchIfMissing = true)
    public ReplayGuard guanjinReplayGuard(GuanjinProperties properties, ObjectProvider<ReplayStore> stores) {
        ReplayStore store = stores.getIfAvailable();
        if (store == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_REPLAY_STORE,
                    "=" + properties.getAuthz().getReplayStore().wireName()
                            + " 但容器里没有可用的 ReplayStore/Redis 客户端 bean（契约 §3.5：SDK 不自读 Redis 地址、"
                            + "不降级到 memory）。请提供 StringRedisTemplate，或在 local|test 下显式置 memory");
        }
        return new ReplayGuard(store, properties.getGateway().getReplayWindow());
    }

    /**
     * 启动自检（GJ-CAP-22）：校验配置 → 拉 JWKS → 打印放行清单。失败即启动失败。
     *
     * <p>{@code enabled=false}（仅 local|test）时 SDK 等价"未接入"，此时不拉 JWKS、也不注册过滤器，
     * 但配置校验照做（否则生产误配 {@code enabled=false} 就没人挡了）。
     */
    @Bean
    public GuanjinStartupReport guanjinStartupReport(GuanjinProperties properties, Environment springEnv,
                                                     JwksClient jwksClient,
                                                     AssertionVerifier verifier,
                                                     ObjectProvider<SecurityContextInjector> injectors) {
        GuanjinEnvironment env = GuanjinEnvironment.of(Arrays.asList(springEnv.getActiveProfiles()));
        if (!properties.isEnabled()) {
            // 能走到这里说明 profile 门控已通过（local|test）；生产置 false 会在 validate 里直接失败
            com.guanjin.core.config.GuanjinConfigValidator.validate(properties, env);
            LOG.warn("guanjin.enabled=false（profile={}）：SDK 未接入，不注册过滤器、不拉 JWKS", env);
            return new GuanjinStartupReport(new StartupReport(properties.getPlatform().getBaseUrl(),
                    String.valueOf(properties.resolvedJwksUri()), java.util.Collections.<String>emptyList(),
                    java.util.Collections.<String>emptyList(), java.util.Collections.<String>emptyList(),
                    java.util.Collections.<String>emptyList()));
        }
        // #26：默认 true 却没有 spring-security-core = "配置说要做、实际没做" → 启动失败（不许静默降级）。
        // 合法出路是显式置 false（契约 #26 明文允许的形态：只影响取身份的方式，不跳过验签、不影响判定），
        // 且它**不受 profile 门控、生产也允许**（AC-SDK-02 的门控只管 enabled / authz.enabled）。
        if (properties.isEnabled() && properties.getSecurity().isInjectSecurityContext()
                && injectors.getIfAvailable() == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.SECURITY_INJECT_SECURITY_CONTEXT,
                    "=true（默认）但 classpath 上没有 spring-security-core（或没有可用的 SecurityContextInjector）："
                            + "拒绝「配置说要做、实际没做」。两条修法任选其一："
                            + "① 引入 org.springframework.security:spring-security-core；"
                            + "② 显式置 guanjin.security.inject-security-context=false"
                            + "（契约 #26 明文允许的合法形态：只影响宿主取身份的方式，不跳过验签、不影响判定；"
                            + "不受 profile 门控，生产也允许）");
        }
        if (verifier == null) {
            // 防御性兜底：默认 bean 一定在；宿主若用 @Primary/@Conditional 把它弄没了，这里拒绝启动
            throw new GuanjinConfigException(GuanjinConfigKeys.ENABLED,
                    "=true 但容器里没有 AssertionVerifier bean：拒绝在未验签状态下运行（GJ-CAP-01 无跳过开关）");
        }
        StartupReport report = GuanjinStartupChecker.check(properties, env, jwksClient);
        if (properties.getStartup().isPrintPublicPaths()) {
            LOG.info("guanjin 放行清单（{} 条）：{}", report.publicPaths().size(), report.publicPaths());
        }
        LOG.info("guanjin 启动自检通过：baseUrl={} jwks={} 网关键={}",
                report.platformBaseUrl(), report.jwksUri(), report.gatewayKids());
        for (String w : report.warnings()) {
            LOG.warn("guanjin 风险配置：{}", w);
        }
        return new GuanjinStartupReport(report);
    }

    /** 断言过滤器（D10 六步）：{@code enabled=true} 时注册，顺序在 Spring Security 之前。 */
    @Bean
    @ConditionalOnProperty(prefix = "guanjin", name = "enabled", havingValue = "true", matchIfMissing = true)
    public FilterRegistrationBean<GuanjinAssertionFilter> guanjinAssertionFilter(
            GuanjinProperties properties, AssertionVerifier verifier, ReplayGuard replayGuard,
            PathMatcher pathMatcher, ObjectProvider<SecurityContextInjector> injectors) {
        SecurityContextInjector injector = injectors.getIfAvailable();
        FilterRegistrationBean<GuanjinAssertionFilter> reg =
                new FilterRegistrationBean<GuanjinAssertionFilter>(
                        new GuanjinAssertionFilter(properties, verifier, replayGuard, pathMatcher, injector));
        reg.setOrder(GuanjinAssertionFilter.ORDER);
        reg.addUrlPatterns("/*");
        reg.setName("guanjinAssertionFilter");
        return reg;
    }

    /**
     * 策略拉取（GJ-CAP-21）：默认**不建这个 bean**（{@code agent.pull.enabled=false}，✅C-5 / AC-SDK-10①）。
     * 显式打开时必须有 HMAC 签名器，否则启动失败（不得发未签名回源请求）。
     */
    @Bean(destroyMethod = "close")
    @ConditionalOnProperty(prefix = "guanjin.agent.pull", name = "enabled", havingValue = "true")
    public PolicyPuller guanjinPolicyPuller(GuanjinProperties properties, HttpFetcher fetcher,
                                            ObjectProvider<PullRequestSigner> signers) {
        PullRequestSigner signer = signers.getIfAvailable();
        if (signer == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AGENT_PULL_ENABLED,
                    "=true 但容器里没有 PullRequestSigner bean：拒绝发出未签名的回源请求"
                            + "（GJ-CAP-21 的 HMAC 签名器尚未装配）");
        }
        String uri = properties.resolvedPoliciesUri();
        if (uri == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL, "为空（无法派生 policies 地址）");
        }
        PolicyPuller puller = new PolicyPuller(URI.create(uri),
                properties.getAgent().getPull().getInterval(), fetcher, signer);
        puller.start();
        return puller;
    }

    /**
     * 授权判定装配（GJ-CAP-12）：装载 {@code guanjin.authz.model-conf} + 业务声明的 {@link AuthzModel}。
     *
     * <p>fail-closed：{@code authz.enabled=true} 而容器里没有 {@code AuthzModel} bean → **启动失败**
     * （没有策略就没有判定依据，不能"没配就全放行"）。
     */
    @Bean
    @ConditionalOnProperty(prefix = "guanjin.authz", name = "enabled", havingValue = "true",
            matchIfMissing = true)
    public GuanjinAuthzHolder guanjinAuthzHolder(GuanjinProperties properties,
                                                 ObjectProvider<AuthzModel> models) {
        AuthzModel model = models.getIfAvailable();
        if (model == null) {
            // 失败必须发生在启动期且能自解释（Spec 裁定 2026-11-10）：报错点名两条修法，
            // 接入方看日志就知道怎么改，不需要回来翻文档。
            throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_ENABLED,
                    "=true（默认）但容器里没有 AuthzModel bean：拒绝在没有策略依据的情况下运行。"
                            + "两条修法任选其一：① 声明 AuthzModel bean"
                            + "（AuthzModel.builder()…build()，见 docs/sdk-contract.md §5 接入步骤）；"
                            + "② 仅 local|test profile 可置 guanjin.authz.enabled=false"
                            + "（=不注册判定，绝不是「全部放行」；非 local|test 置 false 同样启动失败）");
        }
        Authz.init(model, properties.getAuthz().getModelConf());
        return new GuanjinAuthzHolder(model, properties.getAuthz().getModelConf());
    }

    /**
     * 身份 → {@code Authentication} 的转换器（GJ-CAP-10 {@code toAuthentication}）。
     * 业务方可提供 {@link com.guanjin.core.authz.GrantedAuthoritiesResolver} bean（GJ-CAP-11，可选）。
     */
    @Bean
    @ConditionalOnMissingBean(GuanjinAuthenticationConverter.class)
    public GuanjinAuthenticationConverter guanjinAuthenticationConverter(
            GuanjinProperties properties,
            ObjectProvider<com.guanjin.core.authz.GrantedAuthoritiesResolver> resolvers) {
        return new GuanjinAuthenticationConverter(properties.getSecurity().getRolePrefix(),
                resolvers.getIfAvailable());
    }

    /**
     * {@code SecurityContext} 注入（GJ-CAP-10 的 Spring 落点）：只在宿主引了 Spring Security
     * 且 {@code inject-security-context=true}（默认）时生效。
     */
    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.security.core.context.SecurityContextHolder")
    static class SecurityContextConfiguration {

        @Bean
        @ConditionalOnMissingBean(SecurityContextInjector.class)
        @ConditionalOnProperty(prefix = "guanjin.security", name = "inject-security-context",
                havingValue = "true", matchIfMissing = true)
        public SecurityContextInjector guanjinSpringSecurityContextInjector(
                GuanjinAuthenticationConverter converter) {
            return new SpringSecurityContextInjector(converter);
        }
    }

    /** memory 实现：仅 {@code replay-store=memory}（profile 门控已在 validate 里挡）。 */
    @Bean
    @ConditionalOnMissingBean(ReplayStore.class)
    @ConditionalOnProperty(prefix = "guanjin.authz", name = "replay-store", havingValue = "memory")
    public ReplayStore guanjinMemoryReplayStore() {
        return new InMemoryReplayStore();
    }

    /**
     * Redis 实现（默认）。{@code @ConditionalOnClass} 用**字符串形式**：宿主没引入 spring-data-redis 时
     * 这个嵌套配置整体不加载（不会 NoClassDefFoundError），随后 ReplayGuard 的检查会给出可读的启动失败原因。
     */
    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.data.redis.core.StringRedisTemplate")
    static class RedisReplayConfiguration {

        @Bean
        @ConditionalOnMissingBean(ReplayStore.class)
        @ConditionalOnProperty(prefix = "guanjin.authz", name = "replay-store", havingValue = "redis",
                matchIfMissing = true)
        public ReplayStore guanjinRedisReplayStore(
                ObjectProvider<org.springframework.data.redis.core.StringRedisTemplate> templates) {
            org.springframework.data.redis.core.StringRedisTemplate template = templates.getIfAvailable();
            if (template == null) {
                throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_REPLAY_STORE,
                        "=redis（默认）但容器里没有 StringRedisTemplate bean：契约 §3.5 要求复用宿主已注入的客户端，"
                                + "SDK 不自己读 Redis 地址、也不降级到 memory");
            }
            return new RedisReplayStore(template);
        }
    }

    /** 授权判定的装配结果（GJ-CAP-12/13）：便于宿主/运维确认"用的是哪份 model.conf、哪些策略"。 */
    public static final class GuanjinAuthzHolder {

        private final AuthzModel model;
        private final String modelConfLocation;

        GuanjinAuthzHolder(AuthzModel model, String modelConfLocation) {
            this.model = model;
            this.modelConfLocation = modelConfLocation;
        }

        public AuthzModel model() {
            return model;
        }

        public String modelConfLocation() {
            return modelConfLocation;
        }
    }

    /**
     * {@code @RequirePermission} 的执行点注册（GJ-CAP-14）：只在 Spring MVC 在 classpath 时生效
     * （{@code spring-webmvc} 是 optional 依赖，宿主没引入就不注册拦截器，不影响验签与身份注入）。
     */
    @Configuration(proxyBeanMethods = false)
    @ConditionalOnClass(name = "org.springframework.web.servlet.HandlerInterceptor")
    static class AuthorizationWebConfiguration {

        @Bean
        @ConditionalOnProperty(prefix = "guanjin.authz", name = "enabled", havingValue = "true",
                matchIfMissing = true)
        public org.springframework.web.servlet.config.annotation.WebMvcConfigurer guanjinAuthzWebMvcConfigurer() {
            return new org.springframework.web.servlet.config.annotation.WebMvcConfigurer() {
                @Override
                public void addInterceptors(
                        org.springframework.web.servlet.config.annotation.InterceptorRegistry registry) {
                    registry.addInterceptor(new GuanjinAuthorizationInterceptor());
                }
            };
        }
    }
}
