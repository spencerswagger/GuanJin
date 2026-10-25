package com.guanjin.sdk.spring;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "guanjin")
public class GuanjinProperties {

    private boolean enabled = true;
    private Platform platform = new Platform();
    private Client client = new Client();
    private Gateway gateway = new Gateway();
    private GuanjinAuthz authz = new GuanjinAuthz();
    private Agent agent = new Agent();
    private Security security = new Security();
    private Startup startup = new Startup();
    private List<String> publicPaths = new ArrayList<>();
    private boolean publicPathsIncludeBuiltin = true;

    private String jwksUri;
    private String issuer;
    private String audience;
    private Duration jwksCacheTtl;
    private String replayStore;

    public boolean isEnabled() {
        return enabled;
    }

    public void setEnabled(boolean enabled) {
        this.enabled = enabled;
    }

    public Platform getPlatform() {
        return platform;
    }

    public void setPlatform(Platform platform) {
        this.platform = platform;
    }

    public Client getClient() {
        return client;
    }

    public void setClient(Client client) {
        this.client = client;
    }

    public Gateway getGateway() {
        return gateway;
    }

    public void setGateway(Gateway gateway) {
        this.gateway = gateway;
    }

    public GuanjinAuthz getAuthz() {
        return authz;
    }

    public void setAuthz(GuanjinAuthz authz) {
        this.authz = authz;
    }

    public Agent getAgent() {
        return agent;
    }

    public void setAgent(Agent agent) {
        this.agent = agent;
    }

    public Security getSecurity() {
        return security;
    }

    public void setSecurity(Security security) {
        this.security = security;
    }

    public Startup getStartup() {
        return startup;
    }

    public void setStartup(Startup startup) {
        this.startup = startup;
    }

    public List<String> getPublicPaths() {
        return publicPaths;
    }

    public void setPublicPaths(List<String> publicPaths) {
        this.publicPaths = publicPaths;
    }

    public boolean isPublicPathsIncludeBuiltin() {
        return publicPathsIncludeBuiltin;
    }

    public void setPublicPathsIncludeBuiltin(boolean publicPathsIncludeBuiltin) {
        this.publicPathsIncludeBuiltin = publicPathsIncludeBuiltin;
    }

    public String getJwksUri() {
        return jwksUri;
    }

    public void setJwksUri(String jwksUri) {
        this.jwksUri = jwksUri;
    }

    public String getIssuer() {
        return issuer;
    }

    public void setIssuer(String issuer) {
        this.issuer = issuer;
    }

    public String getAudience() {
        return audience;
    }

    public void setAudience(String audience) {
        this.audience = audience;
    }

    public Duration getJwksCacheTtl() {
        return jwksCacheTtl;
    }

    public void setJwksCacheTtl(Duration jwksCacheTtl) {
        this.jwksCacheTtl = jwksCacheTtl;
    }

    public String getReplayStore() {
        return replayStore;
    }

    public void setReplayStore(String replayStore) {
        this.replayStore = replayStore;
    }

    public String effectiveAssertionHeader() {
        return gateway.assertionHeader;
    }

    public String effectiveIssuer() {
        return issuer != null ? issuer : gateway.issuer;
    }

    public String effectiveAudience() {
        if (audience != null) {
            return audience;
        }
        if (gateway.aud != null) {
            return gateway.aud;
        }
        return client.id;
    }

    public String effectiveJwksUri() {
        if (jwksUri != null) {
            return jwksUri;
        }
        if (gateway.jwksUri != null) {
            return gateway.jwksUri;
        }
        return platform.baseUrl == null ? null : trimSlash(platform.baseUrl) + "/api/v1/jwks";
    }

    public Duration effectiveJwksCacheTtl() {
        return jwksCacheTtl != null ? jwksCacheTtl : gateway.jwksCacheTtl;
    }

    public String effectiveReplayStore() {
        return replayStore != null ? replayStore : authz.replayStore;
    }

    public String effectiveAgentClientId() {
        return agent.clientId != null ? agent.clientId : client.id;
    }

    public String effectiveAgentSecret() {
        if (agent.hmacSecret != null) {
            return agent.hmacSecret;
        }
        return agent.secret != null ? agent.secret : client.hmacSecret;
    }

    public String effectiveAgentBaseUrl() {
        return agent.baseUrl != null ? agent.baseUrl : platform.baseUrl;
    }

    private static String trimSlash(String url) {
        return url.endsWith("/") ? url.substring(0, url.length() - 1) : url;
    }

    public static class Platform {

        private String baseUrl;
        private Duration connectTimeout = Duration.ofSeconds(2);
        private Duration readTimeout = Duration.ofSeconds(3);

        public String getBaseUrl() {
            return baseUrl;
        }

        public void setBaseUrl(String baseUrl) {
            this.baseUrl = baseUrl;
        }

        public Duration getConnectTimeout() {
            return connectTimeout;
        }

        public void setConnectTimeout(Duration connectTimeout) {
            this.connectTimeout = connectTimeout;
        }

        public Duration getReadTimeout() {
            return readTimeout;
        }

        public void setReadTimeout(Duration readTimeout) {
            this.readTimeout = readTimeout;
        }
    }

    public static class Client {

        private String id;
        private String hmacSecret;

        public String getId() {
            return id;
        }

        public void setId(String id) {
            this.id = id;
        }

        public String getHmacSecret() {
            return hmacSecret;
        }

        public void setHmacSecret(String hmacSecret) {
            this.hmacSecret = hmacSecret;
        }
    }

    public static class Gateway {

        private String assertionHeader = "X-GuanJin-Assertion";
        private String issuer = "guanjin-gateway";
        private String aud;
        private Duration clockSkew = Duration.ofSeconds(60);
        private Duration replayWindow = Duration.ofSeconds(60);
        private String jwksUri;
        private Duration jwksCacheTtl = Duration.ofMinutes(10);
        private boolean jwksUnknownKidForceRefresh = true;

        public String getAssertionHeader() {
            return assertionHeader;
        }

        public void setAssertionHeader(String assertionHeader) {
            this.assertionHeader = assertionHeader;
        }

        public String getIssuer() {
            return issuer;
        }

        public void setIssuer(String issuer) {
            this.issuer = issuer;
        }

        public String getAud() {
            return aud;
        }

        public void setAud(String aud) {
            this.aud = aud;
        }

        public Duration getClockSkew() {
            return clockSkew;
        }

        public void setClockSkew(Duration clockSkew) {
            this.clockSkew = clockSkew;
        }

        public Duration getReplayWindow() {
            return replayWindow;
        }

        public void setReplayWindow(Duration replayWindow) {
            this.replayWindow = replayWindow;
        }

        public String getJwksUri() {
            return jwksUri;
        }

        public void setJwksUri(String jwksUri) {
            this.jwksUri = jwksUri;
        }

        public Duration getJwksCacheTtl() {
            return jwksCacheTtl;
        }

        public void setJwksCacheTtl(Duration jwksCacheTtl) {
            this.jwksCacheTtl = jwksCacheTtl;
        }

        public boolean isJwksUnknownKidForceRefresh() {
            return jwksUnknownKidForceRefresh;
        }

        public void setJwksUnknownKidForceRefresh(boolean jwksUnknownKidForceRefresh) {
            this.jwksUnknownKidForceRefresh = jwksUnknownKidForceRefresh;
        }
    }

    public static class GuanjinAuthz {

        private boolean enabled = true;
        private String defaultRole = "user";
        private String modelConf = "classpath:/guanjin/model.conf";
        private String replayStore = "memory";
        private Redis redis = new Redis();

        public boolean isEnabled() {
            return enabled;
        }

        public void setEnabled(boolean enabled) {
            this.enabled = enabled;
        }

        public String getDefaultRole() {
            return defaultRole;
        }

        public void setDefaultRole(String defaultRole) {
            this.defaultRole = defaultRole;
        }

        public String getModelConf() {
            return modelConf;
        }

        public void setModelConf(String modelConf) {
            this.modelConf = modelConf;
        }

        public String getReplayStore() {
            return replayStore;
        }

        public void setReplayStore(String replayStore) {
            this.replayStore = replayStore;
        }

        public Redis getRedis() {
            return redis;
        }

        public void setRedis(Redis redis) {
            this.redis = redis;
        }
    }

    public static class Redis {

        private String host = "127.0.0.1";
        private int port = 6379;
        private String password;
        private Duration timeout = Duration.ofSeconds(2);

        public String getHost() {
            return host;
        }

        public void setHost(String host) {
            this.host = host;
        }

        public int getPort() {
            return port;
        }

        public void setPort(int port) {
            this.port = port;
        }

        public String getPassword() {
            return password;
        }

        public void setPassword(String password) {
            this.password = password;
        }

        public Duration getTimeout() {
            return timeout;
        }

        public void setTimeout(Duration timeout) {
            this.timeout = timeout;
        }
    }

    public static class Agent {

        private String clientId;
        private String secret;
        private String hmacSecret;
        private String baseUrl;
        private Report report = new Report();
        private Pull pull = new Pull();

        public String getClientId() {
            return clientId;
        }

        public void setClientId(String clientId) {
            this.clientId = clientId;
        }

        public String getSecret() {
            return secret;
        }

        public void setSecret(String secret) {
            this.secret = secret;
        }

        public String getHmacSecret() {
            return hmacSecret;
        }

        public void setHmacSecret(String hmacSecret) {
            this.hmacSecret = hmacSecret;
        }

        public String getBaseUrl() {
            return baseUrl;
        }

        public void setBaseUrl(String baseUrl) {
            this.baseUrl = baseUrl;
        }

        public Report getReport() {
            return report;
        }

        public void setReport(Report report) {
            this.report = report;
        }

        public Pull getPull() {
            return pull;
        }

        public void setPull(Pull pull) {
            this.pull = pull;
        }

        public static class Report {

            private boolean enabled = true;
            private boolean onStartup = true;
            private boolean onChange = true;

            public boolean isEnabled() {
                return enabled;
            }

            public void setEnabled(boolean enabled) {
                this.enabled = enabled;
            }

            public boolean isOnStartup() {
                return onStartup;
            }

            public void setOnStartup(boolean onStartup) {
                this.onStartup = onStartup;
            }

            public boolean isOnChange() {
                return onChange;
            }

            public void setOnChange(boolean onChange) {
                this.onChange = onChange;
            }
        }

        public static class Pull {

            private boolean enabled = true;
            private Duration interval = Duration.ofSeconds(30);

            public boolean isEnabled() {
                return enabled;
            }

            public void setEnabled(boolean enabled) {
                this.enabled = enabled;
            }

            public Duration getInterval() {
                return interval;
            }

            public void setInterval(Duration interval) {
                this.interval = interval;
            }
        }
    }

    public static class Security {

        private boolean injectSecurityContext = true;
        private String authoritiesResolver;
        private String rolePrefix = "ROLE_";

        public boolean isInjectSecurityContext() {
            return injectSecurityContext;
        }

        public void setInjectSecurityContext(boolean injectSecurityContext) {
            this.injectSecurityContext = injectSecurityContext;
        }

        public String getAuthoritiesResolver() {
            return authoritiesResolver;
        }

        public void setAuthoritiesResolver(String authoritiesResolver) {
            this.authoritiesResolver = authoritiesResolver;
        }

        public String getRolePrefix() {
            return rolePrefix;
        }

        public void setRolePrefix(String rolePrefix) {
            this.rolePrefix = rolePrefix;
        }
    }

    public static class Startup {

        private boolean printPublicPaths = true;

        public boolean isPrintPublicPaths() {
            return printPublicPaths;
        }

        public void setPrintPublicPaths(boolean printPublicPaths) {
            this.printPublicPaths = printPublicPaths;
        }
    }
}
