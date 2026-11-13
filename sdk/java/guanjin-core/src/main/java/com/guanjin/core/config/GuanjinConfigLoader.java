package com.guanjin.core.config;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.TreeSet;

/**
 * 非 Spring 入口的配置装载（契约 §3.6 的 Java 第 ②/③ 条：core 自带校验，覆盖"非 Spring 入口"）。
 *
 * <p>严格白名单：只有 29 个键存在。出现**未知键**或**已废止键**（{@code guanjin.mode} /
 * {@code guanjin.direct.*}）一律抛 {@link GuanjinConfigException} 并**报出键名**——拼错一个字母
 * 必须启动失败，不能静默失效。
 *
 * <p>环境变量形态按契约 §1.1 机械转换：{@code guanjin.gateway.jwks-uri} →
 * {@code GUANJIN_GATEWAY_JWKS_URI}（去前缀、`.`/`-` → `_`、全大写）。
 */
public final class GuanjinConfigLoader {

    /** 环境变量前缀（契约 §1.1）。 */
    public static final String ENV_PREFIX = "GUANJIN_";

    private GuanjinConfigLoader() {
    }

    /** 从规范键名 → 值装载（键如 {@code guanjin.client.id}）。 */
    public static GuanjinConfig fromMap(Map<String, String> in) {
        GuanjinConfig cfg = new GuanjinConfig();
        if (in == null || in.isEmpty()) {
            return cfg;
        }
        Map<String, String> normalized = new LinkedHashMap<String, String>();
        for (Map.Entry<String, String> e : in.entrySet()) {
            normalized.put(e.getKey() == null ? "" : e.getKey().trim(), e.getValue());
        }
        assertWhitelisted(normalized.keySet());
        for (Map.Entry<String, String> e : normalized.entrySet()) {
            apply(cfg, e.getKey(), e.getValue());
        }
        return cfg;
    }

    /**
     * 从 {@code GUANJIN_*} 环境变量装载。
     *
     * <p>{@code GUANJIN_ENV} / {@code GUANJIN_*_DSN} 这类是**运行时环境变量**、不是 29 项配置键，
     * 显式跳过（与 Go 侧 {@code LoadFromEnv} 同一口径）。
     */
    public static GuanjinConfig fromEnv(Map<String, String> env) {
        Map<String, String> in = new LinkedHashMap<String, String>();
        if (env != null) {
            for (Map.Entry<String, String> e : env.entrySet()) {
                String name = e.getKey();
                if (name == null || !name.startsWith(ENV_PREFIX)) {
                    continue;
                }
                if ("GUANJIN_ENV".equals(name) || name.endsWith("_DSN") || name.startsWith("GUANJIN_DB")) {
                    continue;
                }
                in.put(envNameToKey(name), e.getValue());
            }
        }
        return fromMap(in);
    }

    /** 键 → 环境变量名（契约 §1.1 机械转换）。 */
    public static String envNameOf(String key) {
        String s = key.startsWith("guanjin.") ? key.substring("guanjin.".length()) : key;
        s = s.replace('.', '_').replace('-', '_');
        return ENV_PREFIX + s.toUpperCase(Locale.ROOT);
    }

    /**
     * 环境变量名 → 键：靠 29 项白名单逐项比对反解（唯一可靠，避免 {@code -}/{@code .} 混淆）。
     * 白名单外返回原样的点分形式，随后由 {@link #assertWhitelisted} 报错并列出。
     */
    static String envNameToKey(String envName) {
        for (String k : GuanjinConfigKeys.ALL) {
            if (envNameOf(k).equals(envName)) {
                return k;
            }
        }
        return envName.substring(ENV_PREFIX.length()).replace('_', '.').toLowerCase(Locale.ROOT);
    }

    /** 未知键 / 废止键一律失败，并报出键名（契约 §3.6）。 */
    static void assertWhitelisted(Iterable<String> keys) {
        List<String> unknown = new ArrayList<String>();
        for (String k : keys) {
            if (GuanjinConfigKeys.isRemoved(k)) {
                throw new GuanjinConfigException(k,
                        "是已废止键（契约 §3.3：direct 形态已删除，不得出现、也不得用等价开关；AC-SDK-01）");
            }
            if (!GuanjinConfigKeys.isDelivered(k)) {
                unknown.add(k);
            }
        }
        if (!unknown.isEmpty()) {
            throw new GuanjinConfigException(unknown.get(0),
                    "是未知键（只有 " + GuanjinConfigKeys.ALL.size() + " 项交付集，见 docs/sdk-contract.md §3.2；"
                            + "AC-FC-05）。全部未知键=" + new TreeSet<String>(unknown));
        }
    }

    private static void apply(GuanjinConfig cfg, String key, String raw) {
        if (GuanjinConfigKeys.ENABLED.equals(key)) {
            cfg.setEnabled(parseBool(key, raw));
        } else if (GuanjinConfigKeys.PLATFORM_BASE_URL.equals(key)) {
            cfg.getPlatform().setBaseUrl(raw);
        } else if (GuanjinConfigKeys.PLATFORM_CONNECT_TIMEOUT.equals(key)) {
            cfg.getPlatform().setConnectTimeout(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.PLATFORM_READ_TIMEOUT.equals(key)) {
            cfg.getPlatform().setReadTimeout(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.CLIENT_ID.equals(key)) {
            cfg.getClient().setId(raw);
        } else if (GuanjinConfigKeys.CLIENT_HMAC_SECRET.equals(key)) {
            cfg.getClient().setHmacSecret(raw);
        } else if (GuanjinConfigKeys.GATEWAY_ASSERTION_HEADER.equals(key)) {
            cfg.getGateway().setAssertionHeader(raw);
        } else if (GuanjinConfigKeys.GATEWAY_ISSUER.equals(key)) {
            cfg.getGateway().setIssuer(raw);
        } else if (GuanjinConfigKeys.GATEWAY_AUD.equals(key)) {
            cfg.getGateway().setAud(raw);
        } else if (GuanjinConfigKeys.GATEWAY_CLOCK_SKEW.equals(key)) {
            cfg.getGateway().setClockSkew(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.GATEWAY_REPLAY_WINDOW.equals(key)) {
            cfg.getGateway().setReplayWindow(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.GATEWAY_JWKS_URI.equals(key)) {
            cfg.getGateway().setJwksUri(raw);
        } else if (GuanjinConfigKeys.GATEWAY_JWKS_CACHE_TTL.equals(key)) {
            cfg.getGateway().setJwksCacheTtl(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.GATEWAY_JWKS_UNKNOWN_KID_FORCE_REFRESH.equals(key)) {
            cfg.getGateway().setJwksUnknownKidForceRefresh(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AUTHZ_ENABLED.equals(key)) {
            cfg.getAuthz().setEnabled(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AUTHZ_DEFAULT_ROLE.equals(key)) {
            cfg.getAuthz().setDefaultRole(raw);
        } else if (GuanjinConfigKeys.AUTHZ_MODEL_CONF.equals(key)) {
            cfg.getAuthz().setModelConf(raw);
        } else if (GuanjinConfigKeys.AUTHZ_REPLAY_STORE.equals(key)) {
            cfg.getAuthz().setReplayStore(ReplayStoreType.fromWire(raw));
        } else if (GuanjinConfigKeys.AGENT_REPORT_ENABLED.equals(key)) {
            cfg.getAgent().getReport().setEnabled(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AGENT_REPORT_ON_STARTUP.equals(key)) {
            cfg.getAgent().getReport().setOnStartup(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AGENT_REPORT_ON_CHANGE.equals(key)) {
            cfg.getAgent().getReport().setOnChange(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AGENT_PULL_ENABLED.equals(key)) {
            cfg.getAgent().getPull().setEnabled(parseBool(key, raw));
        } else if (GuanjinConfigKeys.AGENT_PULL_INTERVAL.equals(key)) {
            cfg.getAgent().getPull().setInterval(parseDuration(key, raw));
        } else if (GuanjinConfigKeys.PUBLIC_PATHS.equals(key)) {
            cfg.setPublicPaths(parseList(raw));
        } else if (GuanjinConfigKeys.PUBLIC_PATHS_INCLUDE_BUILTIN.equals(key)) {
            cfg.setPublicPathsIncludeBuiltin(parseBool(key, raw));
        } else if (GuanjinConfigKeys.SECURITY_INJECT_SECURITY_CONTEXT.equals(key)) {
            cfg.getSecurity().setInjectSecurityContext(parseBool(key, raw));
        } else if (GuanjinConfigKeys.SECURITY_AUTHORITIES_RESOLVER.equals(key)) {
            cfg.getSecurity().setAuthoritiesResolver(raw);
        } else if (GuanjinConfigKeys.SECURITY_ROLE_PREFIX.equals(key)) {
            cfg.getSecurity().setRolePrefix(raw);
        } else if (GuanjinConfigKeys.STARTUP_PRINT_PUBLIC_PATHS.equals(key)) {
            cfg.getStartup().setPrintPublicPaths(parseBool(key, raw));
        } else {
            // assertWhitelisted 已挡；这里只是防御性兜底（新增键必须同时改白名单）
            throw new GuanjinConfigException(key, "未实现装载（白名单与装载逻辑不同步）");
        }
    }

    private static boolean parseBool(String key, String raw) {
        String v = raw == null ? "" : raw.trim().toLowerCase(Locale.ROOT);
        if ("true".equals(v)) {
            return true;
        }
        if ("false".equals(v)) {
            return false;
        }
        throw new GuanjinConfigException(key, "取不到 bool（只接受 true|false），得到 \"" + raw + "\"");
    }

    /**
     * duration 解析：**严格按契约 §3.2.1**（合法形态唯一 = {@code trim} 后
     * {@code ^[0-9]+(ms|s|m|h)$}）。
     *
     * <p>本方法只是转调 {@link GuanjinConfigValidator#parseDuration}，保证
     * 「Spring 绑定期」与「非 Spring 装载」用的**只有一份**判定；历史上的宽松分支
     * （纯数字按毫秒、{@code d} 按天、整串转小写）已按 2026-11-13 裁决删除。
     */
    static Duration parseDuration(String key, String raw) {
        return GuanjinConfigValidator.parseDuration(key, raw);
    }

    private static List<String> parseList(String raw) {
        List<String> out = new ArrayList<String>();
        if (raw == null) {
            return out;
        }
        String v = raw.trim();
        if (v.startsWith("[")) {
            v = v.substring(1);
        }
        if (v.endsWith("]")) {
            v = v.substring(0, v.length() - 1);
        }
        for (String part : Arrays.asList(v.split(","))) {
            String p = part.trim();
            if (p.length() >= 2 && (p.startsWith("\"") && p.endsWith("\"")) || (p.startsWith("'") && p.endsWith("'"))) {
                p = p.substring(1, p.length() - 1);
            }
            if (!p.isEmpty()) {
                out.add(p);
            }
        }
        return out;
    }
}
