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

    private static String k() {
        return "guanjin.platform.connect-timeout";
    }
}
