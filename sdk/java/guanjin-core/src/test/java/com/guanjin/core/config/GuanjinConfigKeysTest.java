package com.guanjin.core.config;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * AC-SDK-07（配置交付集 = 29 项，连续编号 1..29）+ AC-SDK-01（不存在 mode/direct 键）。
 */
class GuanjinConfigKeysTest {

    @Test
    void AC_SDK_07_deliveredSetIsExactly29WithContiguousNumbering() {
        assertEquals(29, GuanjinConfigKeys.ALL.size(), "交付集必须恰好 29 项（D6-1）");
        assertEquals(29, GuanjinConfigKeys.DESCRIPTORS.size());

        List<Integer> indexes = new ArrayList<Integer>();
        for (GuanjinConfigKeys.KeyDescriptor d : GuanjinConfigKeys.DESCRIPTORS) {
            indexes.add(d.index());
            assertEquals(GuanjinConfigKeys.ALL.get(d.index() - 1), d.key(),
                    "描述符顺序必须等于编号顺序：" + d.key());
        }
        for (int i = 1; i <= 29; i++) {
            assertEquals(Integer.valueOf(i), indexes.get(i - 1), "编号必须连续 1..29（缺号不补 key）");
        }
        // 键名唯一
        Set<String> unique = new HashSet<String>(GuanjinConfigKeys.ALL);
        assertEquals(29, unique.size(), "29 个键不得重复");
    }

    @Test
    void AC_SDK_01_noModeOrDirectKeysInDeliveredSet() {
        for (String removed : GuanjinConfigKeys.REMOVED) {
            assertFalse(GuanjinConfigKeys.ALL.contains(removed), "废止键不得出现在交付集：" + removed);
            assertTrue(GuanjinConfigKeys.isRemoved(removed));
        }
        assertEquals(3, GuanjinConfigKeys.REMOVED.size(), "§3.3 恰好 3 个废止键");
        for (String key : GuanjinConfigKeys.ALL) {
            assertFalse(key.equals("guanjin.mode") || key.endsWith(".mode"), "交付集不得出现 mode 键：" + key);
            assertFalse(key.startsWith("guanjin.direct."), "交付集不得出现 direct 语义：" + key);
            assertTrue(key.startsWith("guanjin."), "键必须 guanjin. 前缀：" + key);
        }
    }

    @Test
    void AC_SDK_07_javaPropertyPathMatchesContractJavaColumn() {
        // 抽查关键项：契约 §3.2 的 "Java 8" 列（点分 camelCase）
        assertProperty("guanjin.platform.base-url", "platform.baseUrl");
        assertProperty("guanjin.client.hmac-secret", "client.hmacSecret");
        assertProperty("guanjin.gateway.jwks-uri", "gateway.jwksUri");
        assertProperty("guanjin.gateway.jwks-unknown-kid-force-refresh", "gateway.jwksUnknownKidForceRefresh");
        assertProperty("guanjin.authz.replay-store", "authz.replayStore");
        assertProperty("guanjin.agent.pull.enabled", "agent.pull.enabled");
        assertProperty("guanjin.public-paths", "publicPaths");
        assertProperty("guanjin.security.inject-security-context", "security.injectSecurityContext");
        assertProperty("guanjin.startup.print-public-paths", "startup.printPublicPaths");
    }

    @Test
    void envNamesFollowMechanicalConversion() {
        assertEquals("GUANJIN_GATEWAY_JWKS_URI",
                GuanjinConfigLoader.envNameOf("guanjin.gateway.jwks-uri"));
        assertEquals("GUANJIN_PUBLIC_PATHS",
                GuanjinConfigLoader.envNameOf("guanjin.public-paths"));
        assertEquals("GUANJIN_ENABLED",
                GuanjinConfigLoader.envNameOf("guanjin.enabled"));
    }

    private static void assertProperty(String key, String propertyPath) {
        for (GuanjinConfigKeys.KeyDescriptor d : GuanjinConfigKeys.DESCRIPTORS) {
            if (d.key().equals(key)) {
                assertEquals(propertyPath, d.propertyPath());
                return;
            }
        }
        throw new AssertionError("缺键：" + key);
    }
}
