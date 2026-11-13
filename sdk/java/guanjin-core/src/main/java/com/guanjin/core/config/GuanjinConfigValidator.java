package com.guanjin.core.config;

import java.net.URI;
import java.net.URISyntaxException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.regex.Pattern;

/**
 * 启动期配置校验（fail-fast）：契约 §3.6 / §4.4 / AC-SDK-01/02/03/04、AC-SESS-05。
 *
 * <p><b>零 Spring</b>：Spring 适配层在 Bean 初始化阶段调用它，非 Spring 入口自己调，
 * 两条入口口径只有这一份。任何一条不满足都抛 {@link GuanjinConfigException}（→ 启动失败、进程非 0），
 * **不是打印后继续跑**。
 *
 * <p>返回的 {@code warnings} 只用于"风险配置告警"（如 {@code clock-skew > 300s}），
 * 告警**不改变**放行/拒绝行为，也不允许任何"配置不合法但先跑起来"的通道。
 */
public final class GuanjinConfigValidator {

    /** 内部断言的生命周期（契约：`exp = iat + 60`），`replay-window` 必须 ≥ 它。 */
    public static final Duration ASSERTION_TTL = Duration.ofSeconds(60);

    /** `clock-skew` 超过它即视为风险配置（契约 #10 要求 Gate 告警）。 */
    public static final Duration CLOCK_SKEW_RISK = Duration.ofSeconds(300);

    /** 断言头不得配置成宿主已用头（契约 #7）。 */
    private static final List<String> HOST_OWNED_HEADERS = Collections.unmodifiableList(Arrays.asList(
            "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length"));

    /**
     * 时长键的**唯一合法形态**（契约 §3.2.1，`FROZEN`）：`trim` 后整串匹配
     * {@code ^[0-9]+(ms|s|m|h)$}。
     *
     * <p>三语言逐字一致（Go {@code durationPattern} / Node 同一正则）；单位**只 4 个**、
     * 小写、非负整数。**大写单位不认**（{@code 1S}/{@code 10M} 非法）。
     */
    public static final String DURATION_PATTERN = "^[0-9]+(ms|s|m|h)$";

    private static final Pattern DURATION = Pattern.compile(DURATION_PATTERN);

    private GuanjinConfigValidator() {
    }

    /**
     * 严格解析时长键（契约 §3.2.1，2026-11-13 裁决「收紧 Java、不放宽契约」）。
     *
     * <p><b>为什么不交给 Spring 的 {@code Duration} 转换器</b>：它接受纯数字（按**毫秒**）、
     * {@code d}（按**天**）和 ISO-8601 {@code PT60S}——那等于自己发明隐式单位，撞 §3.6
     * 「禁隐性逻辑」与 §4.1 M7（三语言同接受/同拒绝）。这里只认 §3.2.1 的 4 个单位，
     * 其余**一律抛 {@link GuanjinConfigException} 并报出键名**，**不得回落默认值**。
     *
     * @param key 契约键名（错误信息必须含它，§3.6）
     * @param raw 原样文本（允许首尾空白，内部空白不认）
     * @return 毫秒精度的 {@link Duration}（无隐式单位）
     * @throws GuanjinConfigException 非 {@code ^[0-9]+(ms|s|m|h)$}，或数值溢出
     */
    public static Duration parseDuration(String key, String raw) {
        String v = trimAsciiWhitespace(raw);
        if (!DURATION.matcher(v).matches()) {
            throw new GuanjinConfigException(key, "取不到时长（契约 §3.2.1：合法形态唯一 = trim 后 "
                    + DURATION_PATTERN + "，单位只 ms|s|m|h、小写、非负整数；纯数字 3000 / d / ns / us / "
                    + "小数 1.5s / 组合 1h30m / ISO-8601 PT60S / 带符号 +60s / 空串 / 内部空白 60 s 一律拒绝），"
                    + "得到 \"" + raw + "\"");
        }
        boolean millis = v.endsWith("ms");
        String number = millis ? v.substring(0, v.length() - 2) : v.substring(0, v.length() - 1);
        char unit = millis ? 'x' : v.charAt(v.length() - 1);
        long unitNanos;
        switch (unit) {
            case 'x': // ms
                unitNanos = 1000000L;
                break;
            case 's':
                unitNanos = 1000000000L;
                break;
            case 'm':
                unitNanos = 60000000000L;
                break;
            case 'h':
                unitNanos = 3600000000000L;
                break;
            default:
                // DURATION 正则已限定单位闭合集；这里只是防御（改动正则而忘了改这里才会到）
                throw new GuanjinConfigException(key, "内部错误：正则与单位换算不同步，得到 \"" + raw + "\"");
        }
        long n;
        try {
            n = Long.parseLong(number);
        } catch (NumberFormatException e) {
            throw new GuanjinConfigException(key, "时长数值本身超出 int64 范围（契约 §3.2.1 上界 = "
                    + "可无损换算成 int64 纳秒），得到 \"" + raw + "\"", e);
        }
        if (n > Long.MAX_VALUE / unitNanos) {
            // 与 Go time.Duration 取交集：不许截断、不许退化成 0（契约 §3.2.1「数值上界」）
            throw new GuanjinConfigException(key, "时长超出可无损换算的 int64 纳秒上界（"
                    + Long.MAX_VALUE + " ns ≈ 292 年）——三语言取交集，不许截断或退化成 0，得到 \"" + raw + "\"");
        }
        return Duration.ofNanos(n * unitNanos);
    }

    /**
     * 契约 §3.2.1 冻结的首尾空白字符集：**只** 4 个 ASCII 空白
     * （空格 {@code U+0020} / {@code \t} {@code U+0009} / {@code \n} {@code U+000A} / {@code \r} {@code U+000D}），
     * 逐个剥、顺序无关。
     *
     * <p><b>不得用语言默认 trim</b>：Java {@code String.trim()} 会多剥 {@code \u0000–\u001F}，
     * Go {@code strings.TrimSpace()} 会多剥 NBSP/全角空格等 Unicode 空白，JS {@code trim()} 亦然——
     * 三者字符集互不相同，用默认实现必然出现「同一份配置某语言收、某语言拒」（撞 §4.1 M7）。
     */
    public static String trimAsciiWhitespace(String raw) {
        if (raw == null) {
            return "";
        }
        int start = 0;
        int end = raw.length();
        while (start < end && isAsciiWhitespace(raw.charAt(start))) {
            start++;
        }
        while (end > start && isAsciiWhitespace(raw.charAt(end - 1))) {
            end--;
        }
        return raw.substring(start, end);
    }

    private static boolean isAsciiWhitespace(char c) {
        return c == ' ' || c == '\t' || c == '\n' || c == '\r';
    }

    /**
     * 该键是否在契约 §3.2 里是 {@code duration} 类型（6 项：#3/#4/#10/#11/#13/#23）。
     *
     * <p>Spring 适配层的绑定期守卫用它决定"哪些原始文本必须按 §3.2.1 校验"——
     * 不硬编码键名，跟着 {@link GuanjinConfigKeys#DESCRIPTORS} 的类型列走。
     */
    public static boolean isDurationKey(String key) {
        for (GuanjinConfigKeys.KeyDescriptor d : GuanjinConfigKeys.DESCRIPTORS) {
            if (d.key().equals(key)) {
                return "duration".equals(d.wireType());
            }
        }
        return false;
    }

    /** 契约 §3.2 里全部 {@code duration} 键（顺序 = 契约编号）。 */
    public static List<String> durationKeys() {
        List<String> out = new ArrayList<String>();
        for (GuanjinConfigKeys.KeyDescriptor d : GuanjinConfigKeys.DESCRIPTORS) {
            if ("duration".equals(d.wireType())) {
                out.add(d.key());
            }
        }
        return Collections.unmodifiableList(out);
    }

    /**
     * 校验并返回告警列表（可能为空）。
     *
     * @throws GuanjinConfigException 任一项不合法（消息里带键名，契约 §3.6）
     */
    public static List<String> validate(GuanjinConfig cfg, GuanjinEnvironment env) {
        if (cfg == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.ENABLED, "配置整体缺失（null）");
        }
        GuanjinEnvironment environment = env == null ? GuanjinEnvironment.production() : env;
        List<String> warnings = new ArrayList<String>();

        // #2 平台地址：必填、可解析、不带尾斜杠、无 query/fragment（AC-SDK-04：缺失或非法 → 启动失败）
        URI base = requirePlatformBaseUrl(cfg.getPlatform().getBaseUrl());

        // #5 / #6 必填
        if (isBlank(cfg.getClient().getId())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.CLIENT_ID,
                    "为空（必填；同时作 aud 期望值与 Agent 面 HMAC 标识）");
        }
        if (isBlank(cfg.getClient().getHmacSecret())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.CLIENT_HMAC_SECRET,
                    "为空（必填；环境变量注入，禁入仓库/禁日志）");
        }

        // C-1 profile 门控（AC-SDK-02）：非 local|test 置 false → 启动失败
        if (!environment.isLocalOrTest()) {
            if (!cfg.isEnabled()) {
                throw new GuanjinConfigException(GuanjinConfigKeys.ENABLED,
                        "只允许在 local|test 置 false（C-1 / AC-SDK-02）。不存在生产关闭验签的开关，"
                                + "false 的语义是\"不注册过滤器（等价未接入）\"，不是\"跳过验签\"。当前 profile="
                                + environment);
            }
            if (!cfg.getAuthz().isEnabled()) {
                throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_ENABLED,
                        "只允许在 local|test 置 false（C-1 / AC-SDK-02）。false 的语义是\"不注册判定"
                                + "（等价未接入）\"，绝不是\"全部放行\"。当前 profile=" + environment);
            }
            // C-4（AC-SESS-05）：多副本用 memory = jti 防重放失效 = 降级放行
            if (cfg.getAuthz().getReplayStore() == ReplayStoreType.MEMORY) {
                throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_REPLAY_STORE,
                        "=memory 只允许在 local|test（C-4 / AC-SESS-05：多副本下 memory = jti 防重放失效）。"
                                + "当前 profile=" + environment);
            }
        }
        if (cfg.getAuthz().getReplayStore() == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_REPLAY_STORE, "只允许 memory|redis，得到 null");
        }

        // #7 断言头不得占用宿主已用头
        String header = cfg.getGateway().getAssertionHeader();
        if (isBlank(header)) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_ASSERTION_HEADER, "为空（默认 X-GuanJin-Assertion）");
        }
        if (HOST_OWNED_HEADERS.contains(header.trim().toLowerCase(Locale.ROOT))) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_ASSERTION_HEADER,
                    "=\"" + header + "\" 是宿主已用头，不得占用（契约 #7）");
        }

        // #8 信任域
        if (isBlank(cfg.getGateway().getIssuer())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_ISSUER,
                    "为空（默认 guanjin-gateway；改值 = 放宽信任域，需 ADR）");
        }

        // #10 时钟偏移：**下界无**——`0s` 合法（= 零容忍：exp/iat 严格边界、不做偏斜补偿；
        // 契约 v16 §8.8 E42，基线 :104 从没写"必须为正"，那是实现自造约束）。负值仍拒：
        // 解析层出不来负数，但编程式赋值可以，且负偏斜会放宽 exp 判定 = 安全洞。
        Duration skew = cfg.getGateway().getClockSkew();
        if (skew == null || skew.isNegative()) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_CLOCK_SKEW,
                    "不得为负（0s = 零容忍，合法；缺省才是默认 60s，两者不得混）");
        }
        if (skew.compareTo(CLOCK_SKEW_RISK) > 0) {
            warnings.add("键 \"" + GuanjinConfigKeys.GATEWAY_CLOCK_SKEW + "\"=" + skew
                    + " 超过 " + CLOCK_SKEW_RISK.getSeconds() + "s：风险配置，会放大 exp 重放口子");
        }

        // #11 replay-window 必须 ≥ 令牌 exp（60s），小于它会漏检重放
        Duration window = cfg.getGateway().getReplayWindow();
        if (window == null || window.compareTo(ASSERTION_TTL) < 0) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_REPLAY_WINDOW,
                    "必须 ≥ " + ASSERTION_TTL.getSeconds() + "s（令牌 exp；小于它会漏检重放），得到 " + window);
        }

        // #12 / C-2：只允许平台基址口径（同源 + 冻结路径）；旧值 /api/v1/jwks 全库不存在 → 首启 404
        requireJwksUri(cfg, base);

        // #13 缓存 TTL
        Duration ttl = cfg.getGateway().getJwksCacheTtl();
        if (ttl == null || ttl.isNegative() || ttl.isZero()) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_CACHE_TTL, "必须为正（默认 10m）");
        }

        // #16 默认角色：权限最小，不得为空
        if (isBlank(cfg.getAuthz().getDefaultRole())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_DEFAULT_ROLE,
                    "为空（权限最小兜底角色，禁\"已登录即全部\"）");
        }

        // #17 model.conf 资源定位不能为空
        if (isBlank(cfg.getAuthz().getModelConf())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AUTHZ_MODEL_CONF,
                    "为空（默认 classpath:/guanjin/model.conf）");
        }

        // #22 / #23 拉取间隔必须为正
        Duration interval = cfg.getAgent().getPull().getInterval();
        if (interval == null || interval.isNegative() || interval.isZero()) {
            throw new GuanjinConfigException(GuanjinConfigKeys.AGENT_PULL_INTERVAL, "必须为正（默认 30s）");
        }

        // #3 / #4 平台超时必须为正（超时即失败，不得重试后放行）
        if (!isPositive(cfg.getPlatform().getConnectTimeout())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_CONNECT_TIMEOUT, "必须为正（默认 2s）");
        }
        if (!isPositive(cfg.getPlatform().getReadTimeout())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_READ_TIMEOUT, "必须为正（默认 3s）");
        }

        // #24 放行即声明：不得把通配/空串写进白名单（deny-by-default）
        List<String> paths = cfg.getPublicPaths();
        if (paths != null) {
            for (String p : paths) {
                if (isBlank(p) || !p.startsWith("/")) {
                    throw new GuanjinConfigException(GuanjinConfigKeys.PUBLIC_PATHS,
                            "含非法条目 \"" + p + "\"（必须以 / 开头，deny-by-default）");
                }
                if (isCatchAll(p)) {
                    throw new GuanjinConfigException(GuanjinConfigKeys.PUBLIC_PATHS,
                            "含全量通配条目 \"" + p + "\"（放行即声明：禁把整个站点写成公开白名单）");
                }
            }
        }

        // 风险配置告警（不影响放行/拒绝）
        if (!cfg.getAgent().getReport().isEnabled()) {
            warnings.add("键 \"" + GuanjinConfigKeys.AGENT_REPORT_ENABLED
                    + "\"=false：平台侧策略快照会失真（审计/回溯能力下降），需报备");
        }
        return Collections.unmodifiableList(warnings);
    }

    /** #2：必填、scheme+host、无 query/fragment；返回解析后的 URI。 */
    private static URI requirePlatformBaseUrl(String raw) {
        if (isBlank(raw)) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "为空（必填、无内置默认值，禁硬编码；AC-SDK-04）");
        }
        if (raw.endsWith("/")) {
            // setter 已归一化；走到这里说明有人绕过 setter（如反射赋值）——不猜，直接失败
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "不得带尾斜杠（契约 #2）：\"" + raw + "\"");
        }
        URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException e) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL, "不是合法 URI：\"" + raw + "\"", e);
        }
        if (uri.getScheme() == null || uri.getHost() == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "需要 scheme://host[:port]（如 https://guanjin.example.com）：\"" + raw + "\"");
        }
        String scheme = uri.getScheme().toLowerCase(Locale.ROOT);
        if (!"http".equals(scheme) && !"https".equals(scheme)) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "只允许 http/https：\"" + raw + "\"");
        }
        if (uri.getRawQuery() != null || uri.getRawFragment() != null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "不得带 query/fragment：\"" + raw + "\"");
        }
        if (uri.getUserInfo() != null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.PLATFORM_BASE_URL,
                    "不得带 userinfo（凭据不进 URL）：\"" + raw + "\"");
        }
        return uri;
    }

    /** #12 / C-2：路径必须是 {@code /.well-known/jwks.json}，且与 base-url 同源（改它 = 换信任源）。 */
    private static void requireJwksUri(GuanjinConfig cfg, URI base) {
        String resolved = cfg.resolvedJwksUri();
        if (isBlank(resolved)) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_URI,
                    "无法派生（platform.base-url 缺失？）");
        }
        URI uri;
        try {
            uri = new URI(resolved);
        } catch (URISyntaxException e) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_URI, "不是合法 URI：\"" + resolved + "\"", e);
        }
        if (uri.getScheme() == null || uri.getHost() == null) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_URI,
                    "需要 scheme://host[:port]/.well-known/jwks.json：\"" + resolved + "\"");
        }
        if (!GuanjinConfigKeys.JWKS_PATH.equals(uri.getRawPath())) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_URI,
                    "只允许路径 " + GuanjinConfigKeys.JWKS_PATH + "（C-2；旧值 /api/v1/jwks 全库不存在，首启 404），"
                            + "得到 \"" + uri.getRawPath() + "\"");
        }
        if (!sameOrigin(base, uri)) {
            throw new GuanjinConfigException(GuanjinConfigKeys.GATEWAY_JWKS_URI,
                    "必须与 platform.base-url 同源（跨域地址 = 换信任源，需 ADR）：\"" + resolved + "\" vs \""
                            + cfg.getPlatform().getBaseUrl() + "\"");
        }
    }

    private static boolean sameOrigin(URI a, URI b) {
        String sa = a.getScheme() == null ? "" : a.getScheme().toLowerCase(Locale.ROOT);
        String sb = b.getScheme() == null ? "" : b.getScheme().toLowerCase(Locale.ROOT);
        String ha = a.getHost() == null ? "" : a.getHost().toLowerCase(Locale.ROOT);
        String hb = b.getHost() == null ? "" : b.getHost().toLowerCase(Locale.ROOT);
        return sa.equals(sb) && ha.equals(hb) && effectivePort(a) == effectivePort(b);
    }

    private static int effectivePort(URI uri) {
        if (uri.getPort() >= 0) {
            return uri.getPort();
        }
        String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
        return "https".equals(scheme) ? 443 : 80;
    }

    private static boolean isPositive(Duration d) {
        return d != null && !d.isNegative() && !d.isZero();
    }

    /** 全量通配（{@code *} / {@code /*} / {@code /**}）= 整站公开，等于没有白名单。 */
    private static boolean isCatchAll(String p) {
        String v = p.trim();
        return "*".equals(v) || "/*".equals(v) || "/**".equals(v) || "**".equals(v);
    }

    private static boolean isBlank(String s) {
        return s == null || s.trim().isEmpty();
    }
}
