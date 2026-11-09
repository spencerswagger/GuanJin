package com.guanjin.core.config;

import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 非 Spring 入口的严格白名单装载（契约 §3.6；AC-SDK-01 / AC-FC-05 的键名断言）。
 */
class GuanjinConfigLoaderTest {

    @Test
    void AC_SDK_01_removedModeKeyFailsWithKeyName() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.mode", "direct");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromMap(in));
        assertEquals("guanjin.mode", e.getKey());
        assertTrue(e.getMessage().contains("guanjin.mode"), e.getMessage());
        assertTrue(e.getMessage().contains("已废止"), e.getMessage());
    }

    @Test
    void AC_SDK_01_removedDirectKeysFail() {
        for (String key : GuanjinConfigKeys.REMOVED) {
            Map<String, String> in = new LinkedHashMap<String, String>();
            in.put(key, "http://x");
            GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                    () -> GuanjinConfigLoader.fromMap(in), key);
            assertEquals(key, e.getKey());
        }
    }

    @Test
    void AC_FC_05_unknownKeyFailsWithKeyName() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.foo", "1");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromMap(in));
        assertEquals("guanjin.foo", e.getKey());
        assertTrue(e.getMessage().contains("未知键"), e.getMessage());
        assertTrue(e.getMessage().contains("29"), e.getMessage());
    }

    @Test
    void loadsAllDefaultsWhenMapEmpty() {
        GuanjinConfig cfg = GuanjinConfigLoader.fromMap(new LinkedHashMap<String, String>());
        assertEquals(true, cfg.isEnabled());
        assertEquals(Duration.ofSeconds(2), cfg.getPlatform().getConnectTimeout());
        assertEquals(Duration.ofSeconds(3), cfg.getPlatform().getReadTimeout());
        assertEquals("X-GuanJin-Assertion", cfg.getGateway().getAssertionHeader());
        assertEquals("guanjin-gateway", cfg.getGateway().getIssuer());
        assertEquals(Duration.ofSeconds(60), cfg.getGateway().getClockSkew());
        assertEquals(Duration.ofSeconds(60), cfg.getGateway().getReplayWindow());
        assertEquals(Duration.ofMinutes(10), cfg.getGateway().getJwksCacheTtl());
        assertEquals(true, cfg.getGateway().isJwksUnknownKidForceRefresh());
        assertEquals("user", cfg.getAuthz().getDefaultRole());
        assertEquals("classpath:/guanjin/model.conf", cfg.getAuthz().getModelConf());
        assertEquals(ReplayStoreType.REDIS, cfg.getAuthz().getReplayStore());
        assertEquals(false, cfg.getAgent().getPull().isEnabled());
        assertEquals(Duration.ofSeconds(30), cfg.getAgent().getPull().getInterval());
        assertEquals(true, cfg.isPublicPathsIncludeBuiltin());
        assertEquals(true, cfg.getSecurity().isInjectSecurityContext());
        assertEquals("ROLE_", cfg.getSecurity().getRolePrefix());
        assertEquals(true, cfg.getStartup().isPrintPublicPaths());
    }

    @Test
    void parsesValuesAndDerivedDefaults() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.platform.base-url", TestConfigs.BASE_URL + "/");
        in.put("guanjin.client.id", "orders");
        in.put("guanjin.client.hmac-secret", "s3cr3t");
        in.put("guanjin.gateway.clock-skew", "90s");
        in.put("guanjin.gateway.replay-window", "120s");
        in.put("guanjin.authz.replay-store", "memory");
        in.put("guanjin.agent.pull.enabled", "true");
        in.put("guanjin.public-paths", "[/assets/*, /healthz]");
        GuanjinConfig cfg = GuanjinConfigLoader.fromMap(in);

        assertEquals(TestConfigs.BASE_URL, cfg.getPlatform().getBaseUrl(), "base-url 去尾斜杠归一化");
        assertEquals(Duration.ofSeconds(90), cfg.getGateway().getClockSkew());
        assertEquals(Duration.ofSeconds(120), cfg.getGateway().getReplayWindow());
        assertEquals(ReplayStoreType.MEMORY, cfg.getAuthz().getReplayStore());
        assertEquals(true, cfg.getAgent().getPull().isEnabled());
        assertEquals(2, cfg.getPublicPaths().size());
        assertEquals("/assets/*", cfg.getPublicPaths().get(0));
        // 派生值
        assertEquals("orders", cfg.resolvedAud(), "aud 默认跟随 client.id");
        assertEquals(TestConfigs.BASE_URL + "/.well-known/jwks.json", cfg.resolvedJwksUri());
        GuanjinConfigValidator.validate(cfg, TestConfigs.local());
    }

    @Test
    void loadsFromGuanjinPrefixedEnvAndSkipsRuntimeVars() {
        Map<String, String> env = new LinkedHashMap<String, String>();
        env.put("GUANJIN_PLATFORM_BASE_URL", TestConfigs.BASE_URL);
        env.put("GUANJIN_CLIENT_ID", "orders");
        env.put("GUANJIN_CLIENT_HMAC_SECRET", "s3cr3t");
        env.put("GUANJIN_GATEWAY_REPLAY_WINDOW", "90s");
        // 运行时变量不是 29 项配置键：必须被跳过（否则每个部署都会启动失败）
        env.put("GUANJIN_ENV", "prod");
        env.put("GUANJIN_TEST_DSN", "postgres://x");
        env.put("PATH", "/usr/bin");

        GuanjinConfig cfg = GuanjinConfigLoader.fromEnv(env);
        assertEquals("orders", cfg.getClient().getId());
        assertEquals(Duration.ofSeconds(90), cfg.getGateway().getReplayWindow());
        GuanjinConfigValidator.validate(cfg, TestConfigs.prod());
    }

    @Test
    void typoedEnvVarFailsInsteadOfSilentlyIgnored() {
        Map<String, String> env = new LinkedHashMap<String, String>();
        env.put("GUANJIN_GATEWAY_JAWKS_URI", "http://x/.well-known/jwks.json");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromEnv(env));
        assertTrue(e.getMessage().contains("未知键"), e.getMessage());
    }

    @Test
    void invalidDurationFailsWithKeyName() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.gateway.clock-skew", "sixty");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromMap(in));
        assertEquals(GuanjinConfigKeys.GATEWAY_CLOCK_SKEW, e.getKey());
    }

    @Test
    void replayStoreOnlyAcceptsMemoryOrRedis() {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.authz.replay-store", "memcached");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigLoader.fromMap(in));
        assertEquals(GuanjinConfigKeys.AUTHZ_REPLAY_STORE, e.getKey());
    }
}
