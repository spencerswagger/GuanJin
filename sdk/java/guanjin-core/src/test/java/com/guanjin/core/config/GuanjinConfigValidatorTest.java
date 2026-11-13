package com.guanjin.core.config;

import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 启动期配置校验：AC-SDK-01（无模式开关）、AC-SDK-02（profile 门控）、AC-SDK-03（jwks-uri 默认）、
 * AC-SDK-04（平台地址必填/合法）、AC-SESS-05（replay-store 默认 redis、memory 仅 local|test）、
 * AC-SDK-10①（默认 redis）。
 */
class GuanjinConfigValidatorTest {

    @Test
    void AC_SDK_03_defaultJwksUriIsWellKnownPathDerivedFromBaseUrl() {
        GuanjinConfig cfg = TestConfigs.minimal();
        assertEquals(TestConfigs.BASE_URL + "/.well-known/jwks.json", cfg.resolvedJwksUri(),
                "默认 jwks-uri = ${platform.base-url}/.well-known/jwks.json（✅C-2）");
        // 旧值 /api/v1/jwks 全库不存在 → 显式配置它必须启动失败（首启 404 不是"警告"）
        cfg.getGateway().setJwksUri(TestConfigs.BASE_URL + "/api/v1/jwks");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertTrue(e.getMessage().contains(GuanjinConfigKeys.GATEWAY_JWKS_URI), e.getMessage());
        assertTrue(e.getMessage().contains("/.well-known/jwks.json"), e.getMessage());
    }

    /**
     * AC-SDK-04 的**附加用例**（不新增 AC ID，依据 Spec 裁定 2026-11-09）：
     * `gateway.jwks-uri` 与 `platform.base-url` 不同源 → **启动失败**。
     *
     * <p>为什么挂 AC-SDK-04：`AC-SDK-03` 只判默认值 + 首启拉取成功、`AC-SDK-04` 只判平台地址缺失/非法，
     * 两条都不覆盖同源；同源校验是 `sdk-contract.md:258`（§3.2 #12 安全约束列）已冻结约束的可执行化，
     * 属加固，不进 CH。
     */
    @Test
    void AC_SDK_04_jwksUriMustBeSameOriginAsPlatform() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setJwksUri("https://evil.example.com/.well-known/jwks.json");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertTrue(e.getMessage().contains("同源"), e.getMessage());
    }

    @Test
    void AC_SDK_04_missingOrIllegalPlatformUrlFailsStartup() {
        GuanjinConfig noBase = TestConfigs.minimal();
        noBase.getPlatform().setBaseUrl(null);
        GuanjinConfigException e1 = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(noBase, TestConfigs.prod()));
        assertEquals(GuanjinConfigKeys.PLATFORM_BASE_URL, e1.getKey());
        assertTrue(e1.getMessage().contains("必填"), e1.getMessage());

        for (String bad : new String[]{"not-a-url", "ftp://guanjin.example.com", "http://", "http://h/?a=1"}) {
            GuanjinConfig cfg = TestConfigs.minimal();
            cfg.getPlatform().setBaseUrl(bad);
            GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                    () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()),
                    "非法平台地址必须启动失败：" + bad);
            assertEquals(GuanjinConfigKeys.PLATFORM_BASE_URL, e.getKey());
        }
    }

    @Test
    void AC_SDK_04_requiredClientFieldsFailStartup() {
        GuanjinConfig noId = TestConfigs.minimal();
        noId.getClient().setId("");
        assertEquals(GuanjinConfigKeys.CLIENT_ID,
                assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.validate(noId, TestConfigs.prod())).getKey());

        GuanjinConfig noSecret = TestConfigs.minimal();
        noSecret.getClient().setHmacSecret(null);
        assertEquals(GuanjinConfigKeys.CLIENT_HMAC_SECRET,
                assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.validate(noSecret, TestConfigs.prod())).getKey());
    }

    @Test
    void AC_SDK_02_enabledFalseOnlyInLocalOrTest() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.setEnabled(false);
        // local|test：允许
        GuanjinConfigValidator.validate(cfg, TestConfigs.local());
        GuanjinConfigValidator.validate(cfg, GuanjinEnvironment.of("test"));
        // 其它 profile：启动失败（不是告警）
        for (GuanjinEnvironment env : new GuanjinEnvironment[]{
                TestConfigs.prod(), GuanjinEnvironment.of("staging"), GuanjinEnvironment.of("prod")}) {
            GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                    () -> GuanjinConfigValidator.validate(cfg, env), "profile=" + env);
            assertEquals(GuanjinConfigKeys.ENABLED, e.getKey());
        }
    }

    @Test
    void AC_SDK_02_authzEnabledFalseOnlyInLocalOrTest() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getAuthz().setEnabled(false);
        GuanjinConfigValidator.validate(cfg, TestConfigs.local());
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertEquals(GuanjinConfigKeys.AUTHZ_ENABLED, e.getKey());
    }

    @Test
    void AC_SESS_05_memoryReplayStoreOnlyInLocalOrTest() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getAuthz().setReplayStore(ReplayStoreType.MEMORY);
        // local|test 允许（但启动日志会告警——告警在适配层打印）
        GuanjinConfigValidator.validate(cfg, TestConfigs.local());
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertEquals(GuanjinConfigKeys.AUTHZ_REPLAY_STORE, e.getKey());
        assertTrue(e.getMessage().contains("memory"), e.getMessage());
    }

    @Test
    void AC_SDK_10_defaultReplayStoreIsRedis() {
        assertEquals(ReplayStoreType.REDIS, new GuanjinConfig().getAuthz().getReplayStore(),
                "默认 replay-store 必须是 redis（✅C-4），不得回落 memory");
        assertEquals("redis", new GuanjinConfig().getAuthz().getReplayStore().wireName());
        // 默认配置在**生产口径**下必须通过（默认即 redis）
        GuanjinConfigValidator.validate(TestConfigs.minimal(), TestConfigs.prod());
    }

    @Test
    void AC_SDK_10_defaultPullEnabledIsFalse() {
        assertEquals(false, new GuanjinConfig().getAgent().getPull().isEnabled(),
                "默认 agent.pull.enabled=false（✅C-5，零回源）");
        assertEquals(Duration.ofSeconds(30), new GuanjinConfig().getAgent().getPull().getInterval());
    }

    @Test
    void replayWindowMustBeAtLeastTokenTtl() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setReplayWindow(Duration.ofSeconds(59));
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertEquals(GuanjinConfigKeys.GATEWAY_REPLAY_WINDOW, e.getKey());
        // 恰好 60s 通过
        cfg.getGateway().setReplayWindow(Duration.ofSeconds(60));
        GuanjinConfigValidator.validate(cfg, TestConfigs.prod());
    }

    @Test
    void assertionHeaderMustNotBeHostOwned() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setAssertionHeader("Authorization");
        GuanjinConfigException e = assertThrows(GuanjinConfigException.class,
                () -> GuanjinConfigValidator.validate(cfg, TestConfigs.prod()));
        assertEquals(GuanjinConfigKeys.GATEWAY_ASSERTION_HEADER, e.getKey());
    }

    @Test
    void publicPathsMustBeExplicitAndNotCatchAll() {
        GuanjinConfig ok = TestConfigs.minimal();
        ok.setPublicPaths(TestConfigs.list("/assets/*", "/healthz"));
        GuanjinConfigValidator.validate(ok, TestConfigs.prod());

        GuanjinConfig relative = TestConfigs.minimal();
        relative.setPublicPaths(TestConfigs.list("orders"));
        assertEquals(GuanjinConfigKeys.PUBLIC_PATHS,
                assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.validate(relative, TestConfigs.prod())).getKey());

        GuanjinConfig catchAll = TestConfigs.minimal();
        catchAll.setPublicPaths(TestConfigs.list("/*"));
        assertEquals(GuanjinConfigKeys.PUBLIC_PATHS,
                assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.validate(catchAll, TestConfigs.prod())).getKey());
    }

    @Test
    void clockSkewOver300sIsWarningNotFailure() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setClockSkew(Duration.ofSeconds(301));
        List<String> warnings = GuanjinConfigValidator.validate(cfg, TestConfigs.prod());
        assertEquals(1, warnings.size(), warnings.toString());
        assertTrue(warnings.get(0).contains(GuanjinConfigKeys.GATEWAY_CLOCK_SKEW));
        // 不影响"能启动"
    }

    /**
     * 契约 v16 §8.8 E42：`clock-skew = 0s` **合法**（= 零容忍），"必须为正"是实现自造的下界 ——
     * 基线 :104 只写「时钟偏移容忍」，没有正值要求；0 只会更严（fail-closed），不产生安全洞。
     */
    @Test
    void clockSkewZeroIsLegalZeroTolerance() {
        GuanjinConfig cfg = TestConfigs.minimal();
        cfg.getGateway().setClockSkew(Duration.ZERO);
        List<String> warnings = GuanjinConfigValidator.validate(cfg, TestConfigs.prod());
        assertTrue(warnings.isEmpty(), "0s 不得告警也不得失败：" + warnings);

        // 负值仍拒（编程式赋值可绕过解析；负偏斜会放宽 exp 判定 = 安全洞）
        GuanjinConfig negative = TestConfigs.minimal();
        negative.getGateway().setClockSkew(Duration.ofSeconds(-1));
        assertEquals(GuanjinConfigKeys.GATEWAY_CLOCK_SKEW,
                assertThrows(GuanjinConfigException.class,
                        () -> GuanjinConfigValidator.validate(negative, TestConfigs.prod())).getKey());
    }
}
