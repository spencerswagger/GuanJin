package com.guanjin.sdk.authz;

import static org.assertj.core.api.Assertions.assertThat;

import com.guanjin.sdk.GuanjinPrincipal;
import java.util.Collections;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class AuthzEngineTest {

    private GuanjinPrincipal principal;
    private AuthzEngine engine;

    @BeforeEach
    void setUp() {
        principal = principal("u1", Collections.emptyList());
    }

    private AuthzEngine engine(AuthzModel model) {
        return new AuthzEngine(model, () -> principal);
    }

    private GuanjinPrincipal principal(String sub, List<String> roles) {
        return new GuanjinPrincipal(sub, "order-service", "order-portal", "zhangsan",
                roles, "AAL1", Collections.emptyList(), 0L, 0L, "jti-1");
    }

    @Test
    void rbacLayer_only_role_permission() {
        AuthzModel model = AuthzModel.builder()
                .role("viewer").can("res", "read")
                .build();
        engine = engine(model);
        principal = principal("u1", List.of("viewer"));

        assertThat(engine.check("res", "read")).isTrue();
        assertThat(engine.check("res", "write")).isFalse();
        assertThat(engine.check("other", "read")).isFalse();
    }

    @Test
    void defaultRole_is_minimal_fallback() {
        AuthzModel model = AuthzModel.builder()
                .role("admin").can("orders", "read", "write")
                .defaultRole("user").can("profile", "read")
                .build();
        engine = engine(model);
        principal = principal("u2", Collections.emptyList());

        assertThat(engine.check("profile", "read")).isTrue();
        assertThat(engine.check("orders", "read")).isFalse();
        assertThat(engine.check("orders", "write")).isFalse();
    }

    @Test
    void ownerLayer_row_level_and_auditor_exemption() {
        AuthzModel model = AuthzModel.builder()
                .role("viewer").can("orders", "read")
                .build();
        engine = engine(model);
        principal = principal("u1", List.of("viewer"));

        assertThat(engine.checkRow("u1")).isTrue();
        assertThat(engine.checkRow("u2")).isFalse();
        assertThat(engine.checkRow(null)).isTrue();

        assertThat(engine.enforcer().enforce(
                new AuthzSubject("u1", 0, Set.of("viewer")), new AuthzObject("orders", "u1", 0), "read")).isTrue();
        assertThat(engine.enforcer().enforce(
                new AuthzSubject("u1", 0, Set.of("viewer")), new AuthzObject("orders", "u2", 0), "read")).isFalse();

        principal = principal("u3", List.of("viewer", "auditor"));
        assertThat(engine.enforcer().enforce(
                new AuthzSubject("u3", 0, Set.of("viewer", "auditor")), new AuthzObject("orders", "u2", 0), "read")).isTrue();
        assertThat(engine.checkRow("u2")).isTrue();
    }

    @Test
    void labelLayer_clearance_gte_label() {
        AuthzModel model = AuthzModel.builder()
                .role("internal_user").withClearance(1).can("docs", "read")
                .role("public_user").withClearance(0).can("docs", "read")
                .dataLabel("docs", 1)
                .build();
        engine = engine(model);

        principal = principal("a", List.of("internal_user"));
        assertThat(engine.check("docs", "read")).isTrue();
        assertThat(engine.checkLabel(1)).isTrue();

        principal = principal("b", List.of("public_user"));
        assertThat(engine.check("docs", "read")).isFalse();
        assertThat(engine.checkLabel(1)).isFalse();
        assertThat(engine.checkLabel(0)).isTrue();
    }

    @Test
    void layers_activate_incrementally() {
        AuthzModel model = AuthzModel.builder()
                .role("worker").can("orders", "read")
                .build();

        principal = principal("u1", List.of("worker"));
        assertThat(new AuthzEngine(model, () -> principal).check("orders", "read")).isTrue();

        AuthzModel withOwner = AuthzModel.builder()
                .role("worker").can("orders", "read")
                .build();
        AuthzEngine ownerEngine = new AuthzEngine(withOwner, () -> principal);
        assertThat(ownerEngine.enforcer().enforce(
                new AuthzSubject("u1", 0, Set.of("worker")), new AuthzObject("orders", "u1", 0), "read")).isTrue();
        assertThat(ownerEngine.enforcer().enforce(
                new AuthzSubject("u1", 0, Set.of("worker")), new AuthzObject("orders", "u2", 0), "read")).isFalse();

        AuthzModel withLabel = AuthzModel.builder()
                .role("worker").withClearance(2).can("orders", "read")
                .dataLabel("orders", 2)
                .build();
        AuthzEngine labelEngine = new AuthzEngine(withLabel, () -> principal);
        assertThat(labelEngine.check("orders", "read")).isTrue();

        principal = principal("u1", List.of("worker"));
        AuthzModel lowClearance = AuthzModel.builder()
                .role("worker").withClearance(1).can("orders", "read")
                .dataLabel("orders", 2)
                .build();
        assertThat(new AuthzEngine(lowClearance, () -> principal).check("orders", "read")).isFalse();
    }

    @Test
    void scope_unrestricted_for_writer_and_auditor() {
        AuthzModel model = AuthzModel.builder()
                .role("order_admin").can("orders", "read", "write")
                .role("order_viewer").can("orders", "read")
                .build();
        engine = engine(model);

        principal = principal("admin", List.of("order_admin"));
        assertThat(engine.scope("orders").isUnrestricted()).isTrue();

        principal = principal("viewer", List.of("order_viewer"));
        ScopeCondition restricted = engine.scope("orders");
        assertThat(restricted.isUnrestricted()).isFalse();
        assertThat(restricted.getOwnerUserId()).isEqualTo("viewer");

        principal = principal("auditor1", List.of("order_viewer", "auditor"));
        assertThat(engine.scope("orders").isUnrestricted()).isTrue();
    }
}
