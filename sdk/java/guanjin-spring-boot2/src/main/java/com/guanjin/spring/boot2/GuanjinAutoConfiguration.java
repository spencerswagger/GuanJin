package com.guanjin.spring.boot2;

import com.guanjin.core.agent.PolicyPuller;
import com.guanjin.core.agent.PullRequestSigner;
import com.guanjin.core.assertion.AssertionVerifier;
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
                                                     ObjectProvider<AssertionVerifier> verifiers) {
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
        AssertionVerifier verifier = verifiers.getIfAvailable();
        if (verifier == null) {
            // 本切片只交付"验签之后"的链路；没有验签器就不许进入"看起来在保护"的状态
            throw new GuanjinConfigException(GuanjinConfigKeys.ENABLED,
                    "=true 但容器里没有 AssertionVerifier bean：拒绝在未验签状态下运行"
                            + "（GJ-CAP-01 无跳过开关；验签实现见后续切片的 Sm2AssertionVerifier）");
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
            PathMatcher pathMatcher) {
        FilterRegistrationBean<GuanjinAssertionFilter> reg =
                new FilterRegistrationBean<GuanjinAssertionFilter>(
                        new GuanjinAssertionFilter(properties, verifier, replayGuard, pathMatcher));
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
}
