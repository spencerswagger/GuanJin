package com.guanjin.core.assertion;

import java.time.Instant;
import java.util.Collections;
import java.util.List;

/**
 * 验签通过后的内部断言（{@code X-GuanJin-Assertion} 的 claim 视图）。
 *
 * <p>claim 集（数据面签发口径）：{@code jti / iss / aud / exp / iat / sub / uname / roles[] /
 * acr / amr[] / cid}，共 11 项。这里只承载 SDK 用得到的字段，多一个都不用。
 */
public final class Assertion {

    private final String jti;
    private final String issuer;
    private final String audience;
    private final Instant expiresAt;
    private final Instant issuedAt;
    private final String subject;
    private final String username;
    private final List<String> roles;
    private final String acr;
    private final List<String> amr;
    private final String clientId;

    public Assertion(String jti, String issuer, String audience, Instant expiresAt, Instant issuedAt,
                     String subject, String username, List<String> roles, String acr,
                     List<String> amr, String clientId) {
        this.jti = jti;
        this.issuer = issuer;
        this.audience = audience;
        this.expiresAt = expiresAt;
        this.issuedAt = issuedAt;
        this.subject = subject;
        this.username = username;
        this.roles = roles == null ? Collections.<String>emptyList() : Collections.unmodifiableList(roles);
        this.acr = acr;
        this.amr = amr == null ? Collections.<String>emptyList() : Collections.unmodifiableList(amr);
        this.clientId = clientId;
    }

    public String jti() {
        return jti;
    }

    public String issuer() {
        return issuer;
    }

    public String audience() {
        return audience;
    }

    public Instant expiresAt() {
        return expiresAt;
    }

    public Instant issuedAt() {
        return issuedAt;
    }

    public String subject() {
        return subject;
    }

    public String username() {
        return username;
    }

    public List<String> roles() {
        return roles;
    }

    public String acr() {
        return acr;
    }

    public List<String> amr() {
        return amr;
    }

    public String clientId() {
        return clientId;
    }

    @Override
    public String toString() {
        // 只打 jti/sub/acr 这类非敏感字段；不打令牌原文
        return "Assertion{jti=" + jti + ", sub=" + subject + ", acr=" + acr + "}";
    }
}
