package com.guanjin.core.config;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;

/**
 * 29 项配置的 Java 形态（字段名 = 契约 §3.2 的 "Java 8" 列，**逐字符照抄**）。
 *
 * <p>本类**零 Spring**：{@code guanjin-spring-boot2} 的 {@code GuanjinProperties} 直接继承它并加
 * {@code @ConfigurationProperties(prefix = "guanjin", ignoreUnknownFields = false)}，非 Spring 入口
 * 用 {@link GuanjinConfigLoader} 从键值对/环境变量装载。两条入口最后都走
 * {@link GuanjinConfigValidator#validate}，口径只有一份。
 *
 * <p>默认值即契约默认值（AC-SDK-07 的反射比对会逐项核）；改这里等于改契约，必须同步
 * {@code docs/sdk-contract.md} §3.2 并经 ADR。
 */
public class GuanjinConfig {

    /** #1 暗开关封口：仅 local|test 可 false，其它 profile false → 启动失败。 */
    private boolean enabled = true;

    private Platform platform = new Platform();
    private Client client = new Client();
    private Gateway gateway = new Gateway();
    private Authz authz = new Authz();
    private Agent agent = new Agent();

    /** #24 放行即声明：空值 = 只有内置白名单（最保守）。 */
    private List<String> publicPaths = new ArrayList<String>();
    /** #25 内置白名单（/healthz 等，不含业务接口）。 */
    private boolean publicPathsIncludeBuiltin = true;

    private Security security = new Security();
    private Startup startup = new Startup();

    // ---- getters / setters（Spring 的 JavaBean 绑定与非 Spring 入口共用） ----

    public boolean isEnabled() {
        return enabled;
    }

    public void setEnabled(boolean enabled) {
        this.enabled = enabled;
    }

    public Platform getPlatform() {
        return platform;
    }

    public void setPlatform(Platform platform) {
        this.platform = platform == null ? new Platform() : platform;
    }

    public Client getClient() {
        return client;
    }

    public void setClient(Client client) {
        this.client = client == null ? new Client() : client;
    }

    public Gateway getGateway() {
        return gateway;
    }

    public void setGateway(Gateway gateway) {
        this.gateway = gateway == null ? new Gateway() : gateway;
    }

    public Authz getAuthz() {
        return authz;
    }

    public void setAuthz(Authz authz) {
        this.authz = authz == null ? new Authz() : authz;
    }

    public Agent getAgent() {
        return agent;
    }

    public void setAgent(Agent agent) {
        this.agent = agent == null ? new Agent() : agent;
    }

    public List<String> getPublicPaths() {
        return publicPaths;
    }

    public void setPublicPaths(List<String> publicPaths) {
        this.publicPaths = publicPaths == null ? new ArrayList<String>() : publicPaths;
    }

    public boolean isPublicPathsIncludeBuiltin() {
        return publicPathsIncludeBuiltin;
    }

    public void setPublicPathsIncludeBuiltin(boolean publicPathsIncludeBuiltin) {
        this.publicPathsIncludeBuiltin = publicPathsIncludeBuiltin;
    }

    public Security getSecurity() {
        return security;
    }

    public void setSecurity(Security security) {
        this.security = security == null ? new Security() : security;
    }

    public Startup getStartup() {
        return startup;
    }

    public void setStartup(Startup startup) {
        this.startup = startup == null ? new Startup() : startup;
    }

    // ---- 派生值（契约里写成 ${...} 的默认） ----

    /** 生效的 JWKS 地址：未显式配置时 = {@code base-url + /.well-known/jwks.json}（#12 ✅C-2）。 */
    public String resolvedJwksUri() {
        String explicit = gateway.getJwksUri();
        if (explicit != null && !explicit.trim().isEmpty()) {
            return explicit.trim();
        }
        String base = platform.getBaseUrl();
        return base == null ? null : base + GuanjinConfigKeys.JWKS_PATH;
    }

    /** 生效的 {@code aud} 期望值：未配置时跟随 {@code client.id}（#9）。 */
    public String resolvedAud() {
        String explicit = gateway.getAud();
        if (explicit != null && !explicit.trim().isEmpty()) {
            return explicit.trim();
        }
        return client.getId();
    }

    /** 策略拉取地址（GJ-CAP-21；只在 {@code agent.pull.enabled=true} 时才会被请求）。 */
    public String resolvedPoliciesUri() {
        String base = platform.getBaseUrl();
        return base == null ? null : base + GuanjinConfigKeys.AGENT_POLICIES_PATH;
    }

    /** #2 平台基址。 */
    public static class Platform {

        /** 必填、无内置默认（禁硬编码）；值即信任源，改它=换平台。 */
        private String baseUrl;
        /** 超时即失败，不得重试后放行。 */
        private Duration connectTimeout = Duration.ofSeconds(2);
        private Duration readTimeout = Duration.ofSeconds(3);

        public String getBaseUrl() {
            return baseUrl;
        }

        /**
         * 去尾斜杠归一化（与 Go 侧 {@code LoadFromMap} 行为一致，契约 #2"不带尾斜杠"）。
         * 归一化写在这里而不是校验里，避免"校验悄悄改值"的隐性逻辑。
         */
        public void setBaseUrl(String baseUrl) {
            this.baseUrl = trimTrailingSlashes(baseUrl);
        }

        public Duration getConnectTimeout() {
            return connectTimeout;
        }

        public void setConnectTimeout(Duration connectTimeout) {
            this.connectTimeout = connectTimeout;
        }

        public Duration getReadTimeout() {
            return readTimeout;
        }

        public void setReadTimeout(Duration readTimeout) {
            this.readTimeout = readTimeout;
        }
    }

    /** #5 / #6 客户端标识与 Agent 面 HMAC 密钥。 */
    public static class Client {

        /** 必填；同时作 {@code aud} 期望值与 HMAC 标识；一系统一 id。 */
        private String id;
        /** 必填；环境变量注入，禁入仓库/禁明文配置文件/禁日志。 */
        private String hmacSecret;

        public String getId() {
            return id;
        }

        public void setId(String id) {
            this.id = id == null ? null : id.trim();
        }

        public String getHmacSecret() {
            return hmacSecret;
        }

        public void setHmacSecret(String hmacSecret) {
            this.hmacSecret = hmacSecret == null ? null : hmacSecret.trim();
        }
    }

    /** #7..#14 断言验签相关。 */
    public static class Gateway {

        /** 改名不改变剥离义务；匹配大小写不敏感。 */
        private String assertionHeader = "X-GuanJin-Assertion";
        /** 改值 = 放宽信任域（接受非本平台签发的断言）→ 需 ADR。 */
        private String issuer = "guanjin-gateway";
        /** 空 = 跟随 client.id（#9）。 */
        private String aud;
        /** 只允许 s 级小值；>300s 视为风险配置（告警，不失败）。 */
        private Duration clockSkew = Duration.ofSeconds(60);
        /** 必须 ≥ 令牌 exp（60s），否则漏检重放 → 启动失败。 */
        private Duration replayWindow = Duration.ofSeconds(60);
        /** 空 = 由 base-url 派生；只允许平台基址 + /.well-known/jwks.json。 */
        private String jwksUri;
        private Duration jwksCacheTtl = Duration.ofMinutes(10);
        /** 未知 kid 强刷一次（应对密钥轮换）；生产保持 true。 */
        private boolean jwksUnknownKidForceRefresh = true;

        public String getAssertionHeader() {
            return assertionHeader;
        }

        public void setAssertionHeader(String assertionHeader) {
            this.assertionHeader = assertionHeader == null ? null : assertionHeader.trim();
        }

        public String getIssuer() {
            return issuer;
        }

        public void setIssuer(String issuer) {
            this.issuer = issuer == null ? null : issuer.trim();
        }

        public String getAud() {
            return aud;
        }

        public void setAud(String aud) {
            this.aud = aud == null ? null : aud.trim();
        }

        public Duration getClockSkew() {
            return clockSkew;
        }

        public void setClockSkew(Duration clockSkew) {
            this.clockSkew = clockSkew;
        }

        public Duration getReplayWindow() {
            return replayWindow;
        }

        public void setReplayWindow(Duration replayWindow) {
            this.replayWindow = replayWindow;
        }

        public String getJwksUri() {
            return jwksUri;
        }

        public void setJwksUri(String jwksUri) {
            this.jwksUri = trimTrailingSlashes(jwksUri);
        }

        public Duration getJwksCacheTtl() {
            return jwksCacheTtl;
        }

        public void setJwksCacheTtl(Duration jwksCacheTtl) {
            this.jwksCacheTtl = jwksCacheTtl;
        }

        public boolean isJwksUnknownKidForceRefresh() {
            return jwksUnknownKidForceRefresh;
        }

        public void setJwksUnknownKidForceRefresh(boolean jwksUnknownKidForceRefresh) {
            this.jwksUnknownKidForceRefresh = jwksUnknownKidForceRefresh;
        }
    }

    /** #15..#18 判定与重放存储。 */
    public static class Authz {

        /** 同 #1 profile 门控：false = 不注册判定（等价未接入），绝不是"全部放行"。 */
        private boolean enabled = true;
        /** 权限最小，禁"已登录即全部"。 */
        private String defaultRole = "user";
        /** 不建议修改（三层模型是合规基线）。 */
        private String modelConf = "classpath:/guanjin/model.conf";
        /** 默认 redis（✅C-4）；memory 仅 local|test。 */
        private ReplayStoreType replayStore = ReplayStoreType.REDIS;

        public boolean isEnabled() {
            return enabled;
        }

        public void setEnabled(boolean enabled) {
            this.enabled = enabled;
        }

        public String getDefaultRole() {
            return defaultRole;
        }

        public void setDefaultRole(String defaultRole) {
            this.defaultRole = defaultRole == null ? null : defaultRole.trim();
        }

        public String getModelConf() {
            return modelConf;
        }

        public void setModelConf(String modelConf) {
            this.modelConf = modelConf == null ? null : modelConf.trim();
        }

        public ReplayStoreType getReplayStore() {
            return replayStore;
        }

        public void setReplayStore(ReplayStoreType replayStore) {
            this.replayStore = replayStore;
        }
    }

    /** #19..#23 Agent 面（上报 / 拉取）。 */
    public static class Agent {

        private Report report = new Report();
        private Pull pull = new Pull();

        public Report getReport() {
            return report;
        }

        public void setReport(Report report) {
            this.report = report == null ? new Report() : report;
        }

        public Pull getPull() {
            return pull;
        }

        public void setPull(Pull pull) {
            this.pull = pull == null ? new Pull() : pull;
        }

        /** #19..#21 策略上报（GJ-CAP-20）。 */
        public static class Report {

            private boolean enabled = true;
            private boolean onStartup = true;
            private boolean onChange = true;

            public boolean isEnabled() {
                return enabled;
            }

            public void setEnabled(boolean enabled) {
                this.enabled = enabled;
            }

            public boolean isOnStartup() {
                return onStartup;
            }

            public void setOnStartup(boolean onStartup) {
                this.onStartup = onStartup;
            }

            public boolean isOnChange() {
                return onChange;
            }

            public void setOnChange(boolean onChange) {
                this.onChange = onChange;
            }
        }

        /** #22 / #23 策略拉取（GJ-CAP-21）：默认 false = 零回源（✅C-5）。 */
        public static class Pull {

            private boolean enabled = false;
            private Duration interval = Duration.ofSeconds(30);

            public boolean isEnabled() {
                return enabled;
            }

            public void setEnabled(boolean enabled) {
                this.enabled = enabled;
            }

            public Duration getInterval() {
                return interval;
            }

            public void setInterval(Duration interval) {
                this.interval = interval;
            }
        }
    }

    /** #26..#28 宿主上下文注入。 */
    public static class Security {

        /** 关掉只影响宿主取身份的方式，不跳过验签、不影响判定。 */
        private boolean injectSecurityContext = true;
        /** Bean 名（Java）；空 = 不映射业务权限（只用角色事实）。 */
        private String authoritiesResolver;
        private String rolePrefix = "ROLE_";

        public boolean isInjectSecurityContext() {
            return injectSecurityContext;
        }

        public void setInjectSecurityContext(boolean injectSecurityContext) {
            this.injectSecurityContext = injectSecurityContext;
        }

        public String getAuthoritiesResolver() {
            return authoritiesResolver;
        }

        public void setAuthoritiesResolver(String authoritiesResolver) {
            this.authoritiesResolver = authoritiesResolver == null ? null : authoritiesResolver.trim();
        }

        public String getRolePrefix() {
            return rolePrefix;
        }

        public void setRolePrefix(String rolePrefix) {
            this.rolePrefix = rolePrefix;
        }
    }

    /** #29 启动打印放行清单（生产建议 true，日志只打路径、不打令牌）。 */
    public static class Startup {

        private boolean printPublicPaths = true;

        public boolean isPrintPublicPaths() {
            return printPublicPaths;
        }

        public void setPrintPublicPaths(boolean printPublicPaths) {
            this.printPublicPaths = printPublicPaths;
        }
    }

    private static String trimTrailingSlashes(String raw) {
        if (raw == null) {
            return null;
        }
        String v = raw.trim();
        while (v.endsWith("/")) {
            v = v.substring(0, v.length() - 1);
        }
        return v;
    }
}
