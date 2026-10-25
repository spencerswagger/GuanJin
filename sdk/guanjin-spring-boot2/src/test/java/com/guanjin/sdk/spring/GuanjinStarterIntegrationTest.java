package com.guanjin.sdk.spring;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.guanjin.sdk.replay.GuanjinInMemoryJtiReplayStore;
import com.guanjin.sdk.replay.GuanjinJtiReplayStore;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.web.servlet.MockMvc;

@SpringBootTest(classes = {TestApplication.class, TestJwksConfiguration.class})
@AutoConfigureMockMvc
class GuanjinStarterIntegrationTest {

    private static final String HEADER = "X-GuanJin-Assertion";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private GuanjinJtiReplayStore replayStore;

    private String adminToken;

    @BeforeEach
    void setUp() {
        if (replayStore instanceof GuanjinInMemoryJtiReplayStore) {
            ((GuanjinInMemoryJtiReplayStore) replayStore).clear();
        }
        adminToken = VectorFixtures.read("assertion.jwt").trim();
    }

    @Test
    void missingAssertionReturns401() throws Exception {
        mockMvc.perform(get("/api/orders/1"))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void publicApiAnnotationSkipsAssertion() throws Exception {
        mockMvc.perform(get("/api/public/ping"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.pong").value(true));
    }

    @Test
    void validAssertionInjectsIdentityAndStripsHeader() throws Exception {
        mockMvc.perform(get("/api/orders/1").header(HEADER, adminToken))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.sub").value("u_12345"))
                .andExpect(jsonPath("$.roles[0]").value("ADMIN"))
                .andExpect(jsonPath("$.acr").value("AAL2"))
                .andExpect(jsonPath("$.downstreamHeader").doesNotExist())
                .andExpect(jsonPath("$.downstreamHeaderNamesContainAssertion").value(false));
    }

    @Test
    void requirePermissionDeniesViewerWriteWith403() throws Exception {
        String viewerToken = VectorFixtures.read("assertion_viewer.jwt").trim();
        mockMvc.perform(post("/api/orders/1").header(HEADER, viewerToken))
                .andExpect(status().isForbidden());
    }

    @Test
    void requirePermissionAllowsAdminWrite() throws Exception {
        mockMvc.perform(post("/api/orders/1").header(HEADER, adminToken))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.written").value(true));
    }

    @Test
    void preAuthorizeStillWorksForAdmin() throws Exception {
        mockMvc.perform(get("/api/admin").header(HEADER, adminToken))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.admin").value(true))
                .andExpect(jsonPath("$.authorities[0].authority").value("ROLE_ADMIN"));
    }

    @Test
    void preAuthorizeDeniesViewer() throws Exception {
        String viewerToken = VectorFixtures.read("assertion_viewer.jwt").trim();
        mockMvc.perform(get("/api/admin").header(HEADER, viewerToken))
                .andExpect(status().isForbidden());
    }

    @Test
    void replayedAssertionIsRejected() throws Exception {
        mockMvc.perform(get("/api/headers").header(HEADER, adminToken))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.assertionHeader").doesNotExist())
                .andExpect(jsonPath("$.identitySub").value("u_12345"));

        mockMvc.perform(get("/api/headers").header(HEADER, adminToken))
                .andExpect(status().isUnauthorized());
    }
}
