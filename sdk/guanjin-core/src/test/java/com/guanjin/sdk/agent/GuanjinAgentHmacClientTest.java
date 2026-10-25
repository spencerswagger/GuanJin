package com.guanjin.sdk.agent;

import static org.assertj.core.api.Assertions.assertThat;

import com.guanjin.sdk.authz.GuanjinAuthzModel;
import com.guanjin.sdk.crypto.GuanjinSm3;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

class GuanjinAgentHmacClientTest {

    private static final byte[] SECRET = "order-service-agent-secret".getBytes(StandardCharsets.UTF_8);
    private static final long TS = 1730000000L;

    private GuanjinAgentHmacClient client(CapturingTransport transport) {
        Clock clock = Clock.fixed(Instant.ofEpochSecond(TS), ZoneOffset.UTC);
        return new GuanjinAgentHmacClient("https://platform.example.com/", "order-service", SECRET,
                transport, clock, () -> "nonce-1");
    }

    @Test
    void canonicalString_matchesSection531() {
        assertThat(GuanjinAgentHmacClient.canonical("POST", "/api/v1/agent/report", "", "deadbeef", TS, "n1"))
                .isEqualTo("POST\n/api/v1/agent/report\n\ndeadbeef\n" + TS + "\nn1");
    }

    @Test
    void report_signsCanonicalAndSendsHeaders() {
        CapturingTransport transport = new CapturingTransport();
        transport.nextPostResponse = "{\"snapshot_id\":\"snap-1\",\"stored_version\":3}";
        GuanjinAgentHmacClient client = client(transport);

        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
                .role("order_admin").can("orders", "read", "write")
                .defaultRole("user").can("profile", "read")
                .dataLabel("orders", 1)
                .build();
        GuanjinPolicyReporter report = GuanjinPolicyReporter.from(model, "order-service", "0.7.0", 1L,
                List.of(new GuanjinInterfaceMapEntry("/orders", "GET", "orders", "read")));

        GuanjinReportResponse response = client.report(report);

        assertThat(transport.lastUrl).isEqualTo("https://platform.example.com/api/v1/agent/report");
        assertThat(transport.lastHeaders).containsKeys(GuanjinAgentHmacClient.HEADER_CLIENT, GuanjinAgentHmacClient.HEADER_TIMESTAMP,
                GuanjinAgentHmacClient.HEADER_NONCE, GuanjinAgentHmacClient.HEADER_SIGNATURE);
        assertThat(transport.lastHeaders.get(GuanjinAgentHmacClient.HEADER_CLIENT)).isEqualTo("order-service");
        assertThat(transport.lastHeaders.get(GuanjinAgentHmacClient.HEADER_TIMESTAMP)).isEqualTo(Long.toString(TS));
        assertThat(transport.lastHeaders.get(GuanjinAgentHmacClient.HEADER_NONCE)).isEqualTo("nonce-1");

        byte[] body = transport.lastBody.getBytes(StandardCharsets.UTF_8);
        String canonical = GuanjinAgentHmacClient.canonical("POST", "/api/v1/agent/report", "",
                GuanjinTransport.sha256Hex(body), TS, "nonce-1");
        String expected = Base64.getEncoder().encodeToString(GuanjinSm3.hmac(SECRET, canonical.getBytes(StandardCharsets.UTF_8)));
        assertThat(transport.lastHeaders.get(GuanjinAgentHmacClient.HEADER_SIGNATURE)).isEqualTo(expected);

        assertThat(transport.lastBody)
                .contains("\"permissions\"")
                .contains("\"roles\"")
                .contains("\"interface_map\"")
                .contains("\"default_role\"")
                .contains("\"label_defs\"")
                .contains("\"orders:read\"")
                .contains("\"path\":\"/orders\"");

        assertThat(response.snapshotId).isEqualTo("snap-1");
        assertThat(response.storedVersion).isEqualTo(3L);
    }

    @Test
    void fetchPolicies_usesSinceVersionQueryAndSignature() {
        CapturingTransport transport = new CapturingTransport();
        transport.nextGetResponse = "{\"version\":7,\"bindings\":[{\"user_id\":\"u1\",\"roles\":[\"order_admin\"]}],"
                + "\"issued_at\":\"2026-01-01T00:00:00Z\"}";
        GuanjinAgentHmacClient client = client(transport);

        GuanjinPoliciesResponse response = client.fetchPolicies(7L);

        assertThat(transport.lastUrl)
                .isEqualTo("https://platform.example.com/api/v1/agent/policies?since_version=7");
        String canonical = GuanjinAgentHmacClient.canonical("GET", "/api/v1/agent/policies", "since_version=7",
                GuanjinTransport.sha256Hex(new byte[0]), TS, "nonce-1");
        String expected = Base64.getEncoder().encodeToString(GuanjinSm3.hmac(SECRET, canonical.getBytes(StandardCharsets.UTF_8)));
        assertThat(transport.lastHeaders.get(GuanjinAgentHmacClient.HEADER_SIGNATURE)).isEqualTo(expected);

        assertThat(response.version).isEqualTo(7L);
        assertThat(response.bindings).hasSize(1);
        assertThat(response.bindings.get(0).userId).isEqualTo("u1");
        assertThat(response.bindings.get(0).roles).containsExactly("order_admin");
    }

    static final class CapturingTransport implements GuanjinTransport {

        String lastUrl;
        Map<String, String> lastHeaders;
        String lastBody;
        String nextPostResponse = "{}";
        String nextGetResponse = "{}";

        @Override
        public String post(String url, Map<String, String> headers, String body) {
            this.lastUrl = url;
            this.lastHeaders = headers;
            this.lastBody = body;
            return nextPostResponse;
        }

        @Override
        public String get(String url, Map<String, String> headers) {
            this.lastUrl = url;
            this.lastHeaders = headers;
            this.lastBody = null;
            return nextGetResponse;
        }
    }
}
