package com.guanjin.core.config;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;

/**
 * 29 项冻结配置键（契约 §3.2，连续编号 1..29）+ 3 项废止键（契约 §3.3）。
 *
 * <p><b>只有这 29 个键存在</b>（契约 §3.6 / Spec R3）：任何拼错、旧键、臆造键都必须变成
 * **可执行的失败**，且必须报出键名。本类是三语言"逐字符相同"的那一份真值。
 *
 * <p>编号空洞说明（ADR-003）：基线原表"号到 32、行剩 29"，缺的 #7/#16/#17 正是 direct 形态的
 * 3 个键（{@code mode} + 两个 {@code direct.user-token-*}）。按 D6-1「禁编造契约」**不补号**，
 * 故本表编号 1..29 连续、没有 #7/#16/#17 对应的键。
 */
public final class GuanjinConfigKeys {

    private GuanjinConfigKeys() {
    }

    // ---- 契约 §3.2 编号 1..29（顺序即编号） ----

    public static final String ENABLED = "guanjin.enabled";
    public static final String PLATFORM_BASE_URL = "guanjin.platform.base-url";
    public static final String PLATFORM_CONNECT_TIMEOUT = "guanjin.platform.connect-timeout";
    public static final String PLATFORM_READ_TIMEOUT = "guanjin.platform.read-timeout";
    public static final String CLIENT_ID = "guanjin.client.id";
    public static final String CLIENT_HMAC_SECRET = "guanjin.client.hmac-secret";
    public static final String GATEWAY_ASSERTION_HEADER = "guanjin.gateway.assertion-header";
    public static final String GATEWAY_ISSUER = "guanjin.gateway.issuer";
    public static final String GATEWAY_AUD = "guanjin.gateway.aud";
    public static final String GATEWAY_CLOCK_SKEW = "guanjin.gateway.clock-skew";
    public static final String GATEWAY_REPLAY_WINDOW = "guanjin.gateway.replay-window";
    public static final String GATEWAY_JWKS_URI = "guanjin.gateway.jwks-uri";
    public static final String GATEWAY_JWKS_CACHE_TTL = "guanjin.gateway.jwks-cache-ttl";
    public static final String GATEWAY_JWKS_UNKNOWN_KID_FORCE_REFRESH =
            "guanjin.gateway.jwks-unknown-kid-force-refresh";
    public static final String AUTHZ_ENABLED = "guanjin.authz.enabled";
    public static final String AUTHZ_DEFAULT_ROLE = "guanjin.authz.default-role";
    public static final String AUTHZ_MODEL_CONF = "guanjin.authz.model-conf";
    public static final String AUTHZ_REPLAY_STORE = "guanjin.authz.replay-store";
    public static final String AGENT_REPORT_ENABLED = "guanjin.agent.report.enabled";
    public static final String AGENT_REPORT_ON_STARTUP = "guanjin.agent.report.on-startup";
    public static final String AGENT_REPORT_ON_CHANGE = "guanjin.agent.report.on-change";
    public static final String AGENT_PULL_ENABLED = "guanjin.agent.pull.enabled";
    public static final String AGENT_PULL_INTERVAL = "guanjin.agent.pull.interval";
    public static final String PUBLIC_PATHS = "guanjin.public-paths";
    public static final String PUBLIC_PATHS_INCLUDE_BUILTIN = "guanjin.public-paths-include-builtin";
    public static final String SECURITY_INJECT_SECURITY_CONTEXT = "guanjin.security.inject-security-context";
    public static final String SECURITY_AUTHORITIES_RESOLVER = "guanjin.security.authorities-resolver";
    public static final String SECURITY_ROLE_PREFIX = "guanjin.security.role-prefix";
    public static final String STARTUP_PRINT_PUBLIC_PATHS = "guanjin.startup.print-public-paths";

    /** JWKS 冻结路径（契约 §3.2 #12 ✅C-2；旧值 {@code /api/v1/jwks} 全库不存在）。 */
    public static final String JWKS_PATH = "/.well-known/jwks.json";

    /** 策略拉取端点（接口清单冻结名；GJ-CAP-21）。 */
    public static final String AGENT_POLICIES_PATH = "/api/v1/agent/policies";

    /** 策略上报端点（接口清单冻结名；GJ-CAP-20）。 */
    public static final String AGENT_REPORT_PATH = "/api/v1/agent/report";

    /** 29 项交付集，顺序 = 契约编号 1..29。 */
    public static final List<String> ALL;

    /** 契约 §3.3 已废止的 3 个键：出现即启动失败（AC-SDK-01 / AC-FC-05）。 */
    public static final List<String> REMOVED;

    /** 29 项描述符（编号 / 键 / Java 绑定路径 / 契约类型 / 契约默认值 / 必填）。 */
    public static final List<KeyDescriptor> DESCRIPTORS;

    static {
        List<String> all = Collections.unmodifiableList(Arrays.asList(
                ENABLED,
                PLATFORM_BASE_URL,
                PLATFORM_CONNECT_TIMEOUT,
                PLATFORM_READ_TIMEOUT,
                CLIENT_ID,
                CLIENT_HMAC_SECRET,
                GATEWAY_ASSERTION_HEADER,
                GATEWAY_ISSUER,
                GATEWAY_AUD,
                GATEWAY_CLOCK_SKEW,
                GATEWAY_REPLAY_WINDOW,
                GATEWAY_JWKS_URI,
                GATEWAY_JWKS_CACHE_TTL,
                GATEWAY_JWKS_UNKNOWN_KID_FORCE_REFRESH,
                AUTHZ_ENABLED,
                AUTHZ_DEFAULT_ROLE,
                AUTHZ_MODEL_CONF,
                AUTHZ_REPLAY_STORE,
                AGENT_REPORT_ENABLED,
                AGENT_REPORT_ON_STARTUP,
                AGENT_REPORT_ON_CHANGE,
                AGENT_PULL_ENABLED,
                AGENT_PULL_INTERVAL,
                PUBLIC_PATHS,
                PUBLIC_PATHS_INCLUDE_BUILTIN,
                SECURITY_INJECT_SECURITY_CONTEXT,
                SECURITY_AUTHORITIES_RESOLVER,
                SECURITY_ROLE_PREFIX,
                STARTUP_PRINT_PUBLIC_PATHS));
        ALL = all;

        REMOVED = Collections.unmodifiableList(Arrays.asList(
                "guanjin.mode",
                "guanjin.direct.user-token-jwks-uri",
                "guanjin.direct.user-token-issuer"));

        List<KeyDescriptor> d = new ArrayList<KeyDescriptor>(29);
        d.add(new KeyDescriptor(1, ENABLED, "enabled", "bool", "true", false));
        d.add(new KeyDescriptor(2, PLATFORM_BASE_URL, "platform.baseUrl", "string(url)", "—", true));
        d.add(new KeyDescriptor(3, PLATFORM_CONNECT_TIMEOUT, "platform.connectTimeout", "duration", "2s", false));
        d.add(new KeyDescriptor(4, PLATFORM_READ_TIMEOUT, "platform.readTimeout", "duration", "3s", false));
        d.add(new KeyDescriptor(5, CLIENT_ID, "client.id", "string", "—", true));
        d.add(new KeyDescriptor(6, CLIENT_HMAC_SECRET, "client.hmacSecret", "string", "—", true));
        d.add(new KeyDescriptor(7, GATEWAY_ASSERTION_HEADER, "gateway.assertionHeader", "string",
                "X-GuanJin-Assertion", false));
        d.add(new KeyDescriptor(8, GATEWAY_ISSUER, "gateway.issuer", "string", "guanjin-gateway", false));
        d.add(new KeyDescriptor(9, GATEWAY_AUD, "gateway.aud", "string", "${client.id}", false));
        d.add(new KeyDescriptor(10, GATEWAY_CLOCK_SKEW, "gateway.clockSkew", "duration", "60s", false));
        d.add(new KeyDescriptor(11, GATEWAY_REPLAY_WINDOW, "gateway.replayWindow", "duration", "60s", false));
        d.add(new KeyDescriptor(12, GATEWAY_JWKS_URI, "gateway.jwksUri", "string(url)",
                "${platform.base-url}/.well-known/jwks.json", false));
        d.add(new KeyDescriptor(13, GATEWAY_JWKS_CACHE_TTL, "gateway.jwksCacheTtl", "duration", "10m", false));
        d.add(new KeyDescriptor(14, GATEWAY_JWKS_UNKNOWN_KID_FORCE_REFRESH,
                "gateway.jwksUnknownKidForceRefresh", "bool", "true", false));
        d.add(new KeyDescriptor(15, AUTHZ_ENABLED, "authz.enabled", "bool", "true", false));
        d.add(new KeyDescriptor(16, AUTHZ_DEFAULT_ROLE, "authz.defaultRole", "string", "user", false));
        d.add(new KeyDescriptor(17, AUTHZ_MODEL_CONF, "authz.modelConf", "resource",
                "classpath:/guanjin/model.conf", false));
        d.add(new KeyDescriptor(18, AUTHZ_REPLAY_STORE, "authz.replayStore", "enum", "redis", false));
        d.add(new KeyDescriptor(19, AGENT_REPORT_ENABLED, "agent.report.enabled", "bool", "true", false));
        d.add(new KeyDescriptor(20, AGENT_REPORT_ON_STARTUP, "agent.report.onStartup", "bool", "true", false));
        d.add(new KeyDescriptor(21, AGENT_REPORT_ON_CHANGE, "agent.report.onChange", "bool", "true", false));
        d.add(new KeyDescriptor(22, AGENT_PULL_ENABLED, "agent.pull.enabled", "bool", "false", false));
        d.add(new KeyDescriptor(23, AGENT_PULL_INTERVAL, "agent.pull.interval", "duration", "30s", false));
        d.add(new KeyDescriptor(24, PUBLIC_PATHS, "publicPaths", "list", "[]", false));
        d.add(new KeyDescriptor(25, PUBLIC_PATHS_INCLUDE_BUILTIN, "publicPathsIncludeBuiltin", "bool",
                "true", false));
        d.add(new KeyDescriptor(26, SECURITY_INJECT_SECURITY_CONTEXT, "security.injectSecurityContext", "bool",
                "true", false));
        d.add(new KeyDescriptor(27, SECURITY_AUTHORITIES_RESOLVER, "security.authoritiesResolver",
                "string(bean/符号名)", "—", false));
        d.add(new KeyDescriptor(28, SECURITY_ROLE_PREFIX, "security.rolePrefix", "string", "ROLE_", false));
        d.add(new KeyDescriptor(29, STARTUP_PRINT_PUBLIC_PATHS, "startup.printPublicPaths", "bool", "true", false));
        DESCRIPTORS = Collections.unmodifiableList(d);
    }

    /** 键是否属于 29 项交付集。 */
    public static boolean isDelivered(String key) {
        return ALL.contains(key);
    }

    /** 键是否属于已废止的 3 项（出现即失败）。 */
    public static boolean isRemoved(String key) {
        return REMOVED.contains(key);
    }

    /**
     * 一项配置的描述符：契约里的编号 / 键 / 类型 / 默认值 / 必填性，以及它在
     * {@link GuanjinConfig} 上的 Java 绑定路径（= 契约 §3.2 的 "Java 8" 列）。
     */
    public static final class KeyDescriptor {

        private final int index;
        private final String key;
        private final String propertyPath;
        private final String wireType;
        private final String contractDefault;
        private final boolean required;

        KeyDescriptor(int index, String key, String propertyPath, String wireType,
                      String contractDefault, boolean required) {
            this.index = index;
            this.key = key;
            this.propertyPath = propertyPath;
            this.wireType = wireType;
            this.contractDefault = contractDefault;
            this.required = required;
        }

        /** 契约编号（1..29，连续）。 */
        public int index() {
            return index;
        }

        /** 规范键，如 {@code guanjin.gateway.replay-window}。 */
        public String key() {
            return key;
        }

        /** Java 绑定路径（去 {@code guanjin.} 前缀的点分 camelCase），如 {@code gateway.replayWindow}。 */
        public String propertyPath() {
            return propertyPath;
        }

        /** 契约类型列原文（{@code bool}/{@code string}/{@code duration}/{@code enum}/{@code list}…）。 */
        public String wireType() {
            return wireType;
        }

        /** 契约默认值列原文；必填项为 {@code —}；派生默认写成 {@code ${...}}。 */
        public String contractDefault() {
            return contractDefault;
        }

        /** 是否必填（契约"必填"列 = 是）。 */
        public boolean required() {
            return required;
        }

        /** 默认值是否由其它键派生（{@code ${...}} 形态）。 */
        public boolean derivedDefault() {
            return contractDefault.startsWith("${");
        }
    }
}
