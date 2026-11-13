package com.guanjin.core.spec;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.guanjin.core.assertion.Assertion;
import com.guanjin.core.assertion.ClaimValidator;
import com.guanjin.core.assertion.Sm2AssertionVerifier;
import com.guanjin.core.authz.Authz;
import com.guanjin.core.authz.AuthzModel;
import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.GuanjinConfigException;
import com.guanjin.core.config.GuanjinConfigLoader;
import com.guanjin.core.config.GuanjinConfigValidator;
import com.guanjin.core.config.GuanjinEnvironment;
import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.http.HttpFetchException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;
import com.guanjin.core.jwks.JwksClient;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import com.guanjin.core.replay.InMemoryReplayStore;
import com.guanjin.core.replay.ReplayGuard;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.net.URI;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * `spec/vectors/*.json` 的**契约一致性**消费者（Java 侧）。
 *
 * <p>向量不是"摆设"：每条都用本 SDK 真实现跑——断言走 {@link Sm2AssertionVerifier} +
 * {@link ClaimValidator}（时钟注入向量里的 reference_now），授权走**真 jcasbin + 真
 * classpath:/guanjin/model.conf**，配置走 {@link GuanjinConfigLoader}。
 *
 * <p>覆盖：assertion-verify.json（13 条，含 **alg 非 SM2 → 拒** 的 D29 核心负例）、
 * authz-decisions.json（14 条）、config-mapping.json（5 组 × 29 键 + REMOVED 键）。
 */
class SpecVectorsConformanceTest {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static JsonNode vector(String name) throws Exception {
        Path p = Paths.get(System.getProperty("user.dir"))
                .resolve("../../../spec/vectors/" + name).normalize();
        assertTrue(Files.isRegularFile(p), "缺向量文件：" + p);
        return MAPPER.readTree(new String(Files.readAllBytes(p), UTF8));
    }

    private static GuanjinConfig assertionConfig(String baseUrl) throws Exception {
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.platform.base-url", baseUrl);
        in.put("guanjin.client.id", "orders");
        in.put("guanjin.client.hmac-secret", "s3cr3t-vector");
        in.put("guanjin.gateway.clock-skew", "60s");
        in.put("guanjin.gateway.replay-window", "60s");
        in.put("guanjin.authz.replay-store", "memory");
        return GuanjinConfigLoader.fromMap(in);
    }

    // ---------------- assertion-verify ----------------

    @Test
    void assertionVectorsMatchJavaVerifierAndClaimValidator() throws Exception {
        JsonNode doc = vector("assertion-verify.json");
        JsonNode jwksDoc = doc.get("jwks");
        Instant refNow = Instant.parse(doc.get("reference_now").asText());
        JsonNode vectors = doc.get("vectors");
        assertTrue(vectors.size() >= 10, "附录 C 要求 ≥10 条，得到 " + vectors.size());

        for (JsonNode v : vectors) {
            final String mode = v.get("jwks_mode").asText("available");
            final byte[] jwksBytes = jwksDoc.toString().getBytes(UTF8);
            HttpFetcher fetcher = new HttpFetcher() {
                @Override
                public HttpResult get(URI uri, Map<String, String> headers) throws HttpFetchException {
                    if ("unavailable".equals(mode)) {
                        throw new HttpFetchException("vector: JWKS 不可达（无缓存）", new java.io.IOException("unavailable"));
                    }
                    return new HttpResult(200, jwksBytes, "application/json");
                }
            };
            GuanjinConfig cfg = assertionConfig("http://127.0.0.1:18080");
            JwksClient jwks = new JwksClient(URI.create("http://127.0.0.1:18080/.well-known/jwks.json"),
                    fetcher, Duration.ofMinutes(10), true);

            String decision = "allow";
            int status = 0;
            String code = "";
            String reason = "";
            Assertion parsed = null;
            try {
                parsed = new Sm2AssertionVerifier(jwks).verify(v.get("token").asText());
                ClaimValidator.validate(parsed, cfg, refNow);
            } catch (GuanjinSdkException e) {
                decision = "reject";
                status = e.getStatus();
                code = e.getCode();
                reason = e.getMessage() == null ? "" : e.getMessage();
            }

            if (v.path("replay_times").asInt(0) > 1) {
                // Java 的 ReplayGuard.firstUse 对重放**抛** 401 TOKEN_INVALID（不是返回 false）
                ReplayGuard guard = new ReplayGuard(new InMemoryReplayStore(), Duration.ofSeconds(60));
                final String replayJti = parsed.jti();
                final Instant replayExp = parsed.expiresAt();
                assertTrue(guard.firstUse(replayJti, replayExp), v.get("vector_id").asText() + " 首次应放行");
                GuanjinSdkException replay = assertThrows(GuanjinSdkException.class,
                        () -> guard.firstUse(replayJti, replayExp),
                        v.get("vector_id").asText() + " 第二次必须判重放");
                assertEquals(401, replay.getStatus());
                assertEquals(ErrorCodes.TOKEN_INVALID, replay.getCode());
                decision = "reject";
                status = 401;
                code = ErrorCodes.TOKEN_INVALID;
            }

            JsonNode expect = v.get("expect");
            if (expect.has("reason_contains")) {
                // D29 判别性断言：拒的理由必须是 alg（不是「找不到键」）。摘掉 alg 闸 → AV-14 会被接受/理由不含 alg。
                String want = expect.get("reason_contains").asText();
                assertEquals("reject", decision, v.get("vector_id").asText() + " 必须拒绝");
                assertTrue(reason.toLowerCase(java.util.Locale.ROOT).contains(want.toLowerCase(java.util.Locale.ROOT)),
                        v.get("vector_id").asText() + " 拒绝理由不含 " + want + "：" + reason);
            }
            assertEquals(expect.get("decision").asText(), decision, v.get("vector_id").asText() + " decision");
            assertEquals(expect.get("status").asInt(0), status, v.get("vector_id").asText() + " status");
            assertEquals(expect.get("code").asText(""), code, v.get("vector_id").asText() + " code");
        }
    }

    // ---------------- authz-decisions ----------------

    @BeforeEach
    void resetContext() {
        GuanjinContextHolder.clear();
    }

    @Test
    void authzVectorsMatchRealJcasbinDecisions() throws Exception {
        JsonNode doc = vector("authz-decisions.json");
        AuthzModel.Builder mb = AuthzModel.builder();
        Map<String, List<String>> groups = new LinkedHashMap<String, List<String>>();
        for (JsonNode role : doc.get("model").get("roles")) {
            AuthzModel.RoleBuilder rb = mb.role(role.get("name").asText());
            for (JsonNode perm : role.get("permissions")) {
                List<String> actions = new ArrayList<String>();
                for (JsonNode a : perm.get("actions")) {
                    actions.add(a.asText());
                }
                rb.can(perm.get("resource").asText(), actions.toArray(new String[0]));
            }
            if (role.has("clearance")) {
                rb.withClearance(role.get("clearance").asInt());
            }
        }
        JsonNode def = doc.get("model").get("default_role");
        AuthzModel.RoleBuilder drb = mb.defaultRole(def.get("name").asText());
        for (JsonNode perm : def.get("permissions")) {
            List<String> actions = new ArrayList<String>();
            for (JsonNode a : perm.get("actions")) {
                actions.add(a.asText());
            }
            drb.can(perm.get("resource").asText(), actions.toArray(new String[0]));
        }
        java.util.Iterator<Map.Entry<String, JsonNode>> it = doc.get("model").get("resource_groups").fields();
        while (it.hasNext()) {
            Map.Entry<String, JsonNode> e = it.next();
            List<String> members = new ArrayList<String>();
            for (JsonNode m : e.getValue()) {
                members.add(m.asText());
            }
            groups.put(e.getKey(), members);
        }
        for (Map.Entry<String, List<String>> e : groups.entrySet()) {
            AuthzModel.GroupBuilder gb = mb.resourceGroup(e.getKey());
            for (String m : e.getValue()) {
                gb.contains(m);
            }
        }
        Authz.init(mb.build(), "classpath:/guanjin/model.conf");

        int n = 0;
        for (JsonNode c : doc.get("cases")) {
            n++;
            JsonNode pj = c.get("principal");
            GuanjinPrincipal p;
            if (pj == null || pj.isNull()) {
                GuanjinContextHolder.clear();
                p = null;
            } else {
                List<String> roles = new ArrayList<String>();
                for (JsonNode r : pj.get("roles")) {
                    roles.add(r.asText());
                }
                p = new GuanjinPrincipal(pj.get("sub").asText(), "alice", roles, "AAL2",
                        Collections.singletonList("pwd"), "orders", "jti-" + n);
                GuanjinContextHolder.set(p);
            }

            JsonNode op = c.get("operation");
            String decision = "allow";
            String code = "";
            try {
                if (p == null) {
                    Authz.require(null, op.get("resource").asText(), op.get("action").asText());
                } else if ("enforce".equals(op.get("kind").asText())) {
                    decision = Authz.enforce(p, op.get("resource").asText(), op.get("action").asText()) ? "allow" : "deny";
                } else if ("require".equals(op.get("kind").asText())) {
                    Authz.require(p, op.get("resource").asText(), op.get("action").asText());
                } else if ("checkRow".equals(op.get("kind").asText())) {
                    Authz.checkRow(op.get("owner_id").asText());
                } else if ("checkLabel".equals(op.get("kind").asText())) {
                    Authz.checkLabel(op.get("obj_level").asInt());
                } else {
                    throw new IllegalStateException("未知 operation.kind: " + op.get("kind").asText());
                }
            } catch (GuanjinSdkException e) {
                decision = "deny";
                code = e.getCode();
            }
            assertEquals(c.get("expect").get("decision").asText(), decision, c.get("vector_id").asText() + " decision");
            if (c.get("expect").has("code")) {
                assertEquals(c.get("expect").get("code").asText(), code, c.get("vector_id").asText() + " code");
            }
        }
        assertTrue(n >= 12, "附录 C 要求 ≥12 条，得到 " + n);
    }

    // ---------------- config-mapping ----------------

    /** 该条在 Java 侧的真实行为：装载期 + 校验期两段都看（安全约束只在 validate 里）。 */
    private static String javaBehavior(String key, String value) {
        // 必备键补齐，否则 validate 会因为"缺 base-url"先失败，掩盖本条的真实判定。
        Map<String, String> in = new LinkedHashMap<String, String>();
        in.put("guanjin.platform.base-url", "https://p.example.com");
        in.put("guanjin.client.id", "c1");
        in.put("guanjin.client.hmac-secret", "s");
        in.put(key, value);
        try {
            GuanjinConfig cfg = GuanjinConfigLoader.fromMap(in);
            java.util.List<String> errs = GuanjinConfigValidator.validate(cfg, GuanjinEnvironment.of("local"));
            if (errs == null || errs.isEmpty()) {
                return "accept";
            }
            boolean hasKey = false;
            for (String e : errs) {
                if (e != null && e.contains(key)) {
                    hasKey = true;
                }
            }
            return hasKey ? "reject@validate" : "reject@validate:no-key";
        } catch (RuntimeException e) {
            String msg = e.getMessage() == null ? "" : e.getMessage();
            return msg.contains(key) ? "reject@load" : "reject@load:no-key";
        }
    }

    @Test
    void configDurationRejectCasesMatchContractSection321() throws Exception {
        JsonNode doc = vector("config-mapping.json");
        JsonNode rejects = doc.get("reject_cases");
        assertNotNull(rejects, "config-mapping.json 缺 reject_cases（§3.2.1 的负例向量没了）");
        assertTrue(rejects.size() > 0, "reject_cases 为空");
        // 词表与生成器 scripts/vecgen/config.go 同口径：
        //   accept / reject@load / reject@validate / reject@load:no-key / reject@validate:no-key
        for (JsonNode rc : rejects) {
            String key = rc.get("key").asText();
            String got = javaBehavior(key, rc.get("value").asText());
            String want = rc.get("current").get("java").asText();
            String id = rc.get("vector_id").asText();
            assertEquals(want, got, id + "(" + rc.get("case").asText() + ") 键=" + key
                    + " 值=" + rc.get("value").asText() + "：Java 当前行为与向量钉的值不一致 —— 行为已变，"
                    + "请把 config-mapping.json 的 current.java 改成 \"" + got + "\"（并确认错误信息含键名）");
        }
    }

    @Test
    void configPendingCasesMatchContractSection321() throws Exception {
        JsonNode doc = vector("config-mapping.json");
        JsonNode pending = doc.get("pending_cases");
        assertNotNull(pending, "config-mapping.json 缺 pending_cases（契约已定的正向欠账没了：CM-13 clock-skew=0s）");
        assertTrue(pending.size() > 0, "pending_cases 为空");
        for (JsonNode pc : pending) {
            String key = pc.get("key").asText();
            String got = javaBehavior(key, pc.get("value").asText());
            String want = pc.get("current").get("java").asText();
            String id = pc.get("vector_id").asText();
            assertEquals(want, got, id + "(" + pc.get("case").asText() + ") 键=" + key
                    + " 值=" + pc.get("value").asText() + "：Java 当前行为与向量钉的值不一致 —— 若已按契约改好，"
                    + "请把这条**升级成正例**（进 cases，按 " + pc.get("expect_after_fix").asText() + " 断言），不要只改 current");
        }
    }

    @Test
    void configVectorsMatchLoaderOnAll29Keys() throws Exception {
        JsonNode doc = vector("config-mapping.json");
        JsonNode keys = doc.get("keys");
        assertEquals(29, keys.size(), "契约 §3.2 是 29 项");

        for (JsonNode c : doc.get("cases")) {
            Map<String, String> in = new LinkedHashMap<String, String>();
            java.util.Iterator<Map.Entry<String, JsonNode>> it = c.get("input").fields();
            while (it.hasNext()) {
                Map.Entry<String, JsonNode> e = it.next();
                in.put(e.getKey(), e.getValue().asText());
            }
            GuanjinConfig cfg = GuanjinConfigLoader.fromMap(in);
            assertEquals(29, c.get("expect").size(),
                    c.get("vector_id").asText() + " expect 只有 " + c.get("expect").size() + " 个键（契约 29 项）");
            for (JsonNode k : keys) {
                assertTrue(c.get("expect").has(k.get("key").asText()),
                        c.get("vector_id").asText() + " expect 缺键 " + k.get("key").asText());
            }
            Map<String, Object> got = canonical(cfg);
            java.util.Iterator<Map.Entry<String, JsonNode>> ex = c.get("expect").fields();
            while (ex.hasNext()) {
                Map.Entry<String, JsonNode> e = ex.next();
                Object want = MAPPER.convertValue(e.getValue(), Object.class);
                Object actual = got.get(e.getKey());
                assertEquals(normalize(want), normalize(actual),
                        c.get("vector_id").asText() + " 键 " + e.getKey());
            }
            if (c.has("expect_derived")) {
                JsonNode d = c.get("expect_derived");
                if (d.has("jwks_uri") && !d.get("jwks_uri").asText().isEmpty()) {
                    assertEquals(d.get("jwks_uri").asText(), cfg.resolvedJwksUri(), c.get("vector_id").asText() + " 派生 jwks-uri");
                }
                if (d.has("aud") && !d.get("aud").asText().isEmpty()) {
                    assertEquals(d.get("aud").asText(), cfg.resolvedAud(), c.get("vector_id").asText() + " 派生 aud");
                }
            }
        }

        for (JsonNode k : doc.get("removed_keys").get("keys")) {
            Map<String, String> bad = new LinkedHashMap<String, String>();
            bad.put(k.asText(), "x");
            assertThrows(GuanjinConfigException.class, () -> GuanjinConfigLoader.fromMap(bad),
                    "REMOVED 键 " + k.asText() + " 必须出现即失败");
        }
    }

    private static Map<String, Object> canonical(GuanjinConfig c) {
        List<String> paths = c.getPublicPaths() == null ? Collections.<String>emptyList() : c.getPublicPaths();
        Map<String, Object> m = new LinkedHashMap<String, Object>();
        m.put("guanjin.enabled", c.isEnabled());
        m.put("guanjin.platform.base-url", c.getPlatform().getBaseUrl() == null ? "" : c.getPlatform().getBaseUrl());
        m.put("guanjin.platform.connect-timeout", c.getPlatform().getConnectTimeout().toMillis());
        m.put("guanjin.platform.read-timeout", c.getPlatform().getReadTimeout().toMillis());
        m.put("guanjin.client.id", c.getClient().getId() == null ? "" : c.getClient().getId());
        m.put("guanjin.client.hmac-secret", c.getClient().getHmacSecret() == null ? "" : c.getClient().getHmacSecret());
        m.put("guanjin.gateway.assertion-header", c.getGateway().getAssertionHeader() == null ? "" : c.getGateway().getAssertionHeader());
        m.put("guanjin.gateway.issuer", c.getGateway().getIssuer() == null ? "" : c.getGateway().getIssuer());
        m.put("guanjin.gateway.aud", c.getGateway().getAud() == null ? "" : c.getGateway().getAud());
        m.put("guanjin.gateway.clock-skew", c.getGateway().getClockSkew().toMillis());
        m.put("guanjin.gateway.replay-window", c.getGateway().getReplayWindow().toMillis());
        m.put("guanjin.gateway.jwks-uri", c.getGateway().getJwksUri() == null ? "" : c.getGateway().getJwksUri());
        m.put("guanjin.gateway.jwks-cache-ttl", c.getGateway().getJwksCacheTtl().toMillis());
        m.put("guanjin.gateway.jwks-unknown-kid-force-refresh", c.getGateway().isJwksUnknownKidForceRefresh());
        m.put("guanjin.authz.enabled", c.getAuthz().isEnabled());
        m.put("guanjin.authz.default-role", c.getAuthz().getDefaultRole() == null ? "" : c.getAuthz().getDefaultRole());
        m.put("guanjin.authz.model-conf", c.getAuthz().getModelConf() == null ? "" : c.getAuthz().getModelConf());
        m.put("guanjin.authz.replay-store", c.getAuthz().getReplayStore() == null ? "" : c.getAuthz().getReplayStore().wireName());
        m.put("guanjin.agent.report.enabled", c.getAgent().getReport().isEnabled());
        m.put("guanjin.agent.report.on-startup", c.getAgent().getReport().isOnStartup());
        m.put("guanjin.agent.report.on-change", c.getAgent().getReport().isOnChange());
        m.put("guanjin.agent.pull.enabled", c.getAgent().getPull().isEnabled());
        m.put("guanjin.agent.pull.interval", c.getAgent().getPull().getInterval().toMillis());
        m.put("guanjin.public-paths", paths);
        m.put("guanjin.public-paths-include-builtin", c.isPublicPathsIncludeBuiltin());
        m.put("guanjin.security.inject-security-context", c.getSecurity().isInjectSecurityContext());
        m.put("guanjin.security.authorities-resolver", c.getSecurity().getAuthoritiesResolver() == null ? "" : c.getSecurity().getAuthoritiesResolver());
        m.put("guanjin.security.role-prefix", c.getSecurity().getRolePrefix() == null ? "" : c.getSecurity().getRolePrefix());
        m.put("guanjin.startup.print-public-paths", c.getStartup().isPrintPublicPaths());
        return m;
    }

    /** 数字统一到 long（Go 侧 duration 是 int64，Jackson 会给 Integer/Long 两种）。 */
    private static Object normalize(Object v) {
        if (v instanceof Integer) {
            return ((Integer) v).longValue();
        }
        if (v instanceof Number) {
            return ((Number) v).longValue();
        }
        return v;
    }

    @Test
    void vectorsDirectoryIsComplete() throws Exception {
        for (String f : new String[]{"agent-hmac.json", "assertion-verify.json", "authz-decisions.json", "config-mapping.json"}) {
            assertNotNull(vector(f), "缺 " + f);
        }
    }
}
