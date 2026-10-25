package com.guanjin.sdk.agent;

import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.crypto.GuanjinSm3;
import com.nimbusds.jose.util.JSONObjectUtils;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.function.Supplier;

public final class GuanjinAgentHmacClient {

    public static final String PATH_REPORT = "/api/v1/agent/report";
    public static final String PATH_POLICIES = "/api/v1/agent/policies";

    public static final String HEADER_CLIENT = "X-GuanJin-Client";
    public static final String HEADER_TIMESTAMP = "X-GuanJin-Timestamp";
    public static final String HEADER_NONCE = "X-GuanJin-Nonce";
    public static final String HEADER_SIGNATURE = "X-GuanJin-Signature";

    private final String baseUrl;
    private final String clientId;
    private final byte[] secret;
    private final GuanjinTransport transport;
    private final Clock clock;
    private final Supplier<String> nonceSupplier;

    public GuanjinAgentHmacClient(String baseUrl, String clientId, byte[] secret, GuanjinTransport transport) {
        this(baseUrl, clientId, secret, transport, Clock.systemUTC(),
                () -> UUID.randomUUID().toString().replace("-", ""));
    }

    public GuanjinAgentHmacClient(String baseUrl,
                       String clientId,
                       byte[] secret,
                       GuanjinTransport transport,
                       Clock clock,
                       Supplier<String> nonceSupplier) {
        this.baseUrl = trimTrailingSlash(baseUrl);
        this.clientId = clientId;
        this.secret = secret == null ? new byte[0] : secret.clone();
        this.transport = transport;
        this.clock = clock == null ? Clock.systemUTC() : clock;
        this.nonceSupplier = nonceSupplier;
    }

    public GuanjinReportResponse report(GuanjinPolicyReporter report) {
        String json = buildReportJson(report);
        Map<String, String> headers = signedHeaders("POST", PATH_REPORT, "",
                json.getBytes(StandardCharsets.UTF_8));
        headers.put("Content-Type", "application/json;charset=UTF-8");
        try {
            return parseReportResponse(transport.post(baseUrl + PATH_REPORT, headers, json));
        } catch (Exception e) {
            throw new GuanjinDependencyException("Agent 上报失败: " + e.getMessage(), e);
        }
    }

    public GuanjinPoliciesResponse fetchPolicies(Long sinceVersion) {
        String query = sinceVersion == null ? "" : "since_version=" + sinceVersion;
        String url = baseUrl + PATH_POLICIES + (query.isEmpty() ? "" : "?" + query);
        Map<String, String> headers = signedHeaders("GET", PATH_POLICIES, query, new byte[0]);
        try {
            return parsePoliciesResponse(transport.get(url, headers));
        } catch (Exception e) {
            throw new GuanjinDependencyException("Agent 拉取策略失败: " + e.getMessage(), e);
        }
    }

    public Map<String, String> signedHeaders(String method, String path, String sortedQuery, byte[] body) {
        long timestamp = clock.instant().getEpochSecond();
        String nonce = nonceSupplier.get();
        String canonical = canonical(method, path, sortedQuery, GuanjinTransport.sha256Hex(body), timestamp, nonce);
        String signature = Base64.getEncoder().encodeToString(GuanjinSm3.hmac(secret, canonical.getBytes(StandardCharsets.UTF_8)));
        Map<String, String> headers = new LinkedHashMap<>();
        headers.put(HEADER_CLIENT, clientId);
        headers.put(HEADER_TIMESTAMP, Long.toString(timestamp));
        headers.put(HEADER_NONCE, nonce);
        headers.put(HEADER_SIGNATURE, signature);
        return headers;
    }

    public static String canonical(String method, String path, String sortedQuery,
                                   String bodySha256Hex, long timestamp, String nonce) {
        return method + "\n" + path + "\n" + sortedQuery + "\n" + bodySha256Hex + "\n" + timestamp + "\n" + nonce;
    }

    public String buildReportJson(GuanjinPolicyReporter report) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("client_id", report.clientId != null ? report.clientId : clientId);
        body.put("sdk_version", report.sdkVersion);
        body.put("version", report.version);
        body.put("model_conf_digest", report.modelConfDigest);
        body.put("permissions", report.permissions == null ? List.of() : report.permissions);

        List<Object> roles = new ArrayList<>();
        if (report.roles != null) {
            for (GuanjinRoleReport role : report.roles) {
                Map<String, Object> item = new LinkedHashMap<>();
                item.put("role", role.role);
                item.put("permissions", role.permissions == null ? List.of() : role.permissions);
                roles.add(item);
            }
        }
        body.put("roles", roles);

        List<Object> interfaceMap = new ArrayList<>();
        if (report.interfaceMap != null) {
            for (GuanjinInterfaceMapEntry entry : report.interfaceMap) {
                Map<String, Object> item = new LinkedHashMap<>();
                item.put("path", entry.path);
                item.put("method", entry.method);
                item.put("resource", entry.resource);
                item.put("action", entry.action);
                interfaceMap.add(item);
            }
        }
        body.put("interface_map", interfaceMap);

        Map<String, Object> defaultRole = new LinkedHashMap<>();
        defaultRole.put("role", report.defaultRole == null ? null : report.defaultRole.role);
        defaultRole.put("permissions",
                report.defaultRole == null || report.defaultRole.permissions == null
                        ? List.of() : report.defaultRole.permissions);
        body.put("default_role", defaultRole);

        List<Object> labelDefs = new ArrayList<>();
        if (report.labelDefs != null) {
            for (GuanjinLabelDef label : report.labelDefs) {
                Map<String, Object> item = new LinkedHashMap<>();
                item.put("resource", label.resource);
                item.put("level", label.level);
                labelDefs.add(item);
            }
        }
        body.put("label_defs", labelDefs);

        return JSONObjectUtils.toJSONString(body);
    }

    private GuanjinReportResponse parseReportResponse(String json) {
        try {
            Map<String, Object> parsed = JSONObjectUtils.parse(json);
            return new GuanjinReportResponse(asString(parsed.get("snapshot_id")), asLong(parsed.get("stored_version")));
        } catch (Exception e) {
            throw new GuanjinDependencyException("上报响应解析失败: " + e.getMessage(), e);
        }
    }

    private GuanjinPoliciesResponse parsePoliciesResponse(String json) {
        try {
            Map<String, Object> parsed = JSONObjectUtils.parse(json);
            List<GuanjinBinding> bindings = new ArrayList<>();
            Object raw = parsed.get("bindings");
            if (raw instanceof List) {
                for (Object item : (List<?>) raw) {
                    if (item instanceof Map) {
                        Map<?, ?> map = (Map<?, ?>) item;
                        bindings.add(new GuanjinBinding(asString(map.get("user_id")), asStringList(map.get("roles"))));
                    }
                }
            }
            return new GuanjinPoliciesResponse(asLong(parsed.get("version")), bindings, asString(parsed.get("issued_at")));
        } catch (Exception e) {
            throw new GuanjinDependencyException("拉取响应解析失败: " + e.getMessage(), e);
        }
    }

    private static String trimTrailingSlash(String url) {
        if (url == null) {
            return "";
        }
        return url.endsWith("/") ? url.substring(0, url.length() - 1) : url;
    }

    private static String asString(Object value) {
        return value == null ? null : String.valueOf(value);
    }

    private static long asLong(Object value) {
        if (value instanceof Number) {
            return ((Number) value).longValue();
        }
        if (value == null) {
            return 0L;
        }
        try {
            return Long.parseLong(String.valueOf(value));
        } catch (NumberFormatException e) {
            return 0L;
        }
    }

    private static List<String> asStringList(Object value) {
        List<String> out = new ArrayList<>();
        if (value instanceof List) {
            for (Object item : (List<?>) value) {
                if (item != null) {
                    out.add(String.valueOf(item));
                }
            }
        }
        return out;
    }
}
