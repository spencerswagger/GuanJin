package com.guanjin.core.startup;

import java.util.Collections;
import java.util.List;

/** 启动自检的结果（GJ-CAP-22）：全部是"可审计的事实"，不含任何 secret/令牌。 */
public final class StartupReport {

    private final String platformBaseUrl;
    private final String jwksUri;
    private final List<String> jwksKids;
    private final List<String> gatewayKids;
    private final List<String> publicPaths;
    private final List<String> warnings;

    public StartupReport(String platformBaseUrl, String jwksUri, List<String> jwksKids,
                         List<String> gatewayKids, List<String> publicPaths, List<String> warnings) {
        this.platformBaseUrl = platformBaseUrl;
        this.jwksUri = jwksUri;
        this.jwksKids = Collections.unmodifiableList(jwksKids);
        this.gatewayKids = Collections.unmodifiableList(gatewayKids);
        this.publicPaths = Collections.unmodifiableList(publicPaths);
        this.warnings = Collections.unmodifiableList(warnings);
    }

    public String platformBaseUrl() {
        return platformBaseUrl;
    }

    public String jwksUri() {
        return jwksUri;
    }

    /** JWKS 里全部 kid（含 oidc 用途）。 */
    public List<String> jwksKids() {
        return jwksKids;
    }

    /** 网关用途（{@code alg=SM2}，D29 双轨）的 kid，SDK 只用这些验签。 */
    public List<String> gatewayKids() {
        return gatewayKids;
    }

    /** 生效的放行清单（供评审；日志只打路径，不打令牌/secret）。 */
    public List<String> publicPaths() {
        return publicPaths;
    }

    /** 风险配置告警（不改变放行/拒绝行为）。 */
    public List<String> warnings() {
        return warnings;
    }

    @Override
    public String toString() {
        return "StartupReport{baseUrl=" + platformBaseUrl + ", jwks=" + jwksUri
                + ", gatewayKids=" + gatewayKids + ", publicPaths=" + publicPaths
                + ", warnings=" + warnings + "}";
    }
}
