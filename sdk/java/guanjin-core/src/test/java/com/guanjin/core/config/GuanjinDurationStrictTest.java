package com.guanjin.core.config;

import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 契约 §3.2.1 时长键的**严格解析**（`FROZEN`；判据 `AC-SDK-11`）。
 *
 * <p>口径（逐字来自契约，不在这里重新解释）：
 * <ul>
 *   <li>合法形态**唯一**：`trim` 后整串匹配 {@code ^[0-9]+(ms|s|m|h)$}；单位只 {@code ms|s|m|h}、小写、非负整数；</li>
 *   <li>九类必须**启动失败**：纯数字 {@code 3000}、{@code d}、{@code ns}/{@code us}/{@code µs}/{@code μs}、
 *       小数 {@code 1.5s}、组合 {@code 1h30m}、ISO-8601 {@code PT60S}、带符号 {@code +60s}/{@code -1s}、
 *       空串/纯空白、内部空白 {@code 60 s}；</li>
 *   <li>失败必须**报键名**，且**不得回落默认值**。</li>
 * </ul>
 */
class GuanjinDurationStrictTest {

    /** 契约 §3.2 的 6 个 duration 键（#3 / #4 / #10 / #11 / #13 / #23）。 */
    private static final String[] DURATION_KEYS = {
            "guanjin.platform.connect-timeout",
            "guanjin.platform.read-timeout",
            "guanjin.gateway.clock-skew",
            "guanjin.gateway.replay-window",
            "guanjin.gateway.jwks-cache-ttl",
            "guanjin.agent.pull.interval",
    };

    /** 契约 §3.2.1 列出的九类非法形态（外加大小写/裸单位两类防御）。 */
    private static final Map<String, String> ILLEGAL = illegal();

    private static Map<String, String> illegal() {
        Map<String, String> m = new LinkedHashMap<String, String>();
        m.put("①纯数字", "3000");
        m.put("②单位 d", "1d");
        m.put("③单位 ns", "100ns");
        m.put("③单位 us", "100us");
        m.put("③微秒符号 µs", "100µs");
        m.put("③微秒符号 μs", "100μs");
        m.put("④小数", "1.5s");
        m.put("⑤组合", "1h30m");
        m.put("⑥ISO-8601", "PT60S");
        m.put("⑦带符号加", "+60s");
        m.put("⑦带符号减", "-1s");
        m.put("⑧空串", "");
        m.put("⑧纯空白", "   ");
        m.put("⑨内部空白", "60 s");
        m.put("大写单位", "1S");
        m.put("大写单位", "10M");
        m.put("裸单位", "s");
        return m;
    }

    @Test
    void AC_SDK_11_nineIllegalFormsFailOnEveryDurationKey() {
        for (String key : DURATION_KEYS) {
            for (Map.Entry<String, String> c : ILLEGAL.entrySet()) {
                GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.parseDuration(key, c.getValue()),
                        key + " 的 " + c.getKey() + " 形态 \"" + c.getValue() + "\" 必须启动失败（§3.2.1）");
                assertEquals(key, e.getKey(), "错误必须报出键名（§3.6）");
                assertTrue(e.getMessage().contains(key), "错误文案里必须有键名：" + e.getMessage());
                assertTrue(e.getMessage().contains(GuanjinConfigValidator.DURATION_PATTERN),
                        "错误文案应指出唯一合法形态：" + e.getMessage());
            }
        }
    }

    @Test
    void AC_SDK_11_canonicalFormsAreParsedWithoutImplicitUnit() {
        assertEquals(Duration.ofMillis(0), GuanjinConfigValidator.parseDuration(k(), "0ms"));
        assertEquals(Duration.ofMillis(300), GuanjinConfigValidator.parseDuration(k(), "300ms"));
        assertEquals(Duration.ofSeconds(2), GuanjinConfigValidator.parseDuration(k(), "2s"));
        assertEquals(Duration.ofSeconds(60), GuanjinConfigValidator.parseDuration(k(), "60s"));
        assertEquals(Duration.ofMinutes(10), GuanjinConfigValidator.parseDuration(k(), "10m"));
        assertEquals(Duration.ofHours(1), GuanjinConfigValidator.parseDuration(k(), "1h"));
        // 前导零合法（正则就是 [0-9]+）；只允许首尾空白（trim 是显式写进契约的）
        assertEquals(Duration.ofSeconds(7), GuanjinConfigValidator.parseDuration(k(), "007s"));
        assertEquals(Duration.ofSeconds(30), GuanjinConfigValidator.parseDuration(k(), "  30s  "));
    }

    @Test
    void AC_SDK_11_overflowIsRejectedWithKeyName() {
        String key = "guanjin.gateway.jwks-cache-ttl";
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.parseDuration(key, "999999999999999999999s"));
        assertEquals(key, e.getKey());
    }

    @Test
    void AC_SDK_11_trimSetIsFrozenToFourAsciiWhitespace() {
        // 契约 §3.2.1「首尾空白」：只 4 个 ASCII 空白（U+0020 / \t / \n / \r），逐个剥、顺序无关；
        // 其它空白一律不 trim → 落正则外即失败。**不得**用 String.trim()（会多剥 U+0000–U+001F）。
        assertEquals(Duration.ofSeconds(30), GuanjinConfigValidator.parseDuration(k(), "\t30s\r\n"));
        assertEquals(Duration.ofSeconds(2), GuanjinConfigValidator.parseDuration(k(), "\r\n 2s \t"));
        assertEquals(Duration.ofMinutes(10), GuanjinConfigValidator.parseDuration(k(), " 10m "));
        assertEquals(Duration.ofHours(1), GuanjinConfigValidator.parseDuration(k(), "\t\t1h\n\n"));

        Map<String, String> illegal = new LinkedHashMap<String, String>();
        illegal.put("NBSP 前缀 U+00A0", "\u00A02s");
        illegal.put("NBSP 后缀 U+00A0", "2s\u00A0");
        illegal.put("全角空格 U+3000", "\u30002s");
        illegal.put("全角空格后缀 U+3000", "2s\u3000");
        illegal.put("U+2000 前缀", "\u20002s");
        illegal.put("U+2003 后缀", "2s\u2003");
        illegal.put("U+000B 垂直制表", "\u000B30s");
        illegal.put("U+000C 换页", "\u000C30s");
        illegal.put("U+0000 NUL 前缀", "\u000030s");
        illegal.put("NUL 夹在中间", "3\u00000s");
        for (Map.Entry<String, String> e : illegal.entrySet()) {
            GuanjinConfigException ex = assertThrows(GuanjinConfigException.class,
                    () -> GuanjinConfigValidator.parseDuration(k(), e.getValue()),
                    e.getKey() + " 不属于 4 个 ASCII 空白，不得 trim，必须失败");
            assertEquals(k(), ex.getKey(), "错误必须报键名");
        }
    }

    @Test
    void AC_SDK_11_upperBoundIsLosslessInt64Nanos() {
        // 契约 §3.2.1「数值上界」：必须能无损换算成 int64 纳秒（≈292 年，取三语言交集）；
        // 越界 → 启动失败报键名，**不许截断到最大值、不许退化成 0**。
        String key = "guanjin.gateway.jwks-cache-ttl";
        assertEquals(Duration.ofNanos(Long.MAX_VALUE / 3_600_000_000_000L * 3_600_000_000_000L),
                GuanjinConfigValidator.parseDuration(key, "2562047h"));
        assertEquals(Duration.ofSeconds(9223372036L), GuanjinConfigValidator.parseDuration(key, "9223372036s"));
        assertEquals(Duration.ofMillis(9223372036854L), GuanjinConfigValidator.parseDuration(key, "9223372036854ms"));
        assertEquals(Duration.ofMinutes(153722867L), GuanjinConfigValidator.parseDuration(key, "153722867m"));

        String[] illegal = {"2562048h", "153722868m", "9223372037s", "9223372036855ms",
                "9999999999999h", "99999999999999999999s"};
        for (String raw : illegal) {
            GuanjinConfigException ex = assertThrows(GuanjinConfigException.class,
                    () -> GuanjinConfigValidator.parseDuration(key, raw),
                    raw + " 超出 int64 纳秒上界，必须失败（不许截断/退化）");
            assertEquals(key, ex.getKey(), "错误必须报键名");
            assertTrue(ex.getMessage().contains("int64"), "文案应点明 int64 纳秒上界：" + ex.getMessage());
        }
    }

    @Test
    void AC_SDK_11_loaderRejectsIllegalTextAndNeverFallsBackToDefault() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.platform.base-url", "https://gj.example.com");
        in.put("guanjin.client.id", "orders");
        in.put("guanjin.gateway.jwks-cache-ttl", "1h30m");

        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromMap(in));
        assertEquals("guanjin.gateway.jwks-cache-ttl", e.getKey(),
                "组合形态必须失败并报键名，不得回落默认 10m");
        // 默认值是 10m；如果静默回落，这里就会"起得来" —— 用直调确认默认值本身仍然合法
        assertEquals(Duration.ofMinutes(10),
                GuanjinConfigValidator.parseDuration("guanjin.gateway.jwks-cache-ttl", "10m"));
    }

    @Test
    void AC_SDK_11_allSixDurationKeysAreDiscoverableByType() {
        assertEquals(6, GuanjinConfigValidator.durationKeys().size(),
                "契约 §3.2 的 duration 键恰好 6 项（#3/#4/#10/#11/#13/#23）");
        for (String key : DURATION_KEYS) {
            assertTrue(GuanjinConfigValidator.durationKeys().contains(key), key + " 应被识别为 duration 键");
            assertTrue(GuanjinConfigValidator.isDurationKey(key), key + " 应被识别为 duration 键");
            assertNotNull(GuanjinConfigValidator.parseDuration(key, "1s"));
        }
        assertTrue(!GuanjinConfigValidator.isDurationKey("guanjin.client.id"), "非 duration 键不得误判");
    }

    /**
     * 契约 v16 §8.8 E42：`clock-skew=0s` 合法（零容忍），且**显式 0 与缺省必须可区分**——
     * 缺省走默认 60s，显式 `0s` 就是 0（零容忍），不得回落。
     */
    @Test
    void AC_SDK_11_clockSkewZeroIsDistinctFromAbsent() {
        Map<String, String> explicitZero = baseConfig();
        explicitZero.put("guanjin.gateway.clock-skew", "0s");
        GuanjinConfig zero = GuanjinConfigLoader.fromMap(explicitZero);
        assertEquals(Duration.ZERO, zero.getGateway().getClockSkew(),
                "显式 0s 必须如实生效为 0，不得回落到默认 60s");
        // 空 profile 集合 = 生产口径（判严）；0s 必须能过
        GuanjinConfigValidator.validate(zero, GuanjinEnvironment.of(java.util.Collections.<String>emptyList()));

        Map<String, String> absent = baseConfig();
        GuanjinConfig dflt = GuanjinConfigLoader.fromMap(absent);
        assertEquals(Duration.ofSeconds(60), dflt.getGateway().getClockSkew(),
                "缺省必须走默认 60s（与显式 0 区分）");
    }

    private static Map<String, String> baseConfig() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.platform.base-url", "https://gj.example.com");
        in.put("guanjin.client.id", "orders");
        in.put("guanjin.client.hmac-secret", "s3cr3t-test-only-0123456789abcdef");
        in.put("guanjin.gateway.jwks-uri", "https://gj.example.com/.well-known/jwks.json");
        in.put("guanjin.authz.replay-store", "redis");
        in.put("guanjin.gateway.replay-window", "60s");
        return in;
    }

    private static String k() {
        return "guanjin.platform.connect-timeout";
    }
}
