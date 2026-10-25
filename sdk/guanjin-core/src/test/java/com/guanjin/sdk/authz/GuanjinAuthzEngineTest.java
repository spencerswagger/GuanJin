package com.guanjin.sdk.authz;

import static org.assertj.core.api.Assertions.assertThat;

import com.guanjin.sdk.GuanjinIdentity;
import java.util.Collections;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class GuanjinAuthzEngineTest {

    private GuanjinIdentity principal;
    private GuanjinAuthzEngine engine;

    @BeforeEach
    void setUp() {
        principal = principal("u1", Collections.emptyList());
    }

    private GuanjinAuthzEngine engine(GuanjinAuthzModel model) {
        return new GuanjinAuthzEngine(model, () -> principal);
    }

    private GuanjinIdentity principal(String sub, List<String> roles) {
        return new GuanjinIdentity(sub, "order-service", "order-portal", "zhangsan",
                roles, "AAL1", Collections.emptyList(), 0L, 0L, "jti-1");
    }

    @Test
    void rbacLayer_only_role_permission() {
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
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
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
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
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
                .role("viewer").can("orders", "read")
                .build();
        engine = engine(model);
        principal = principal("u1", List.of("viewer"));

        assertThat(engine.checkRow("u1")).isTrue();
        assertThat(engine.checkRow("u2")).isFalse();
        assertThat(engine.checkRow(null)).isTrue();

        assertThat(engine.enforcer().enforce(
                new GuanjinAuthzSubject("u1", 0, Set.of("viewer")), new GuanjinAuthzObject("orders", "u1", 0), "read")).isTrue();
        assertThat(engine.enforcer().enforce(
                new GuanjinAuthzSubject("u1", 0, Set.of("viewer")), new GuanjinAuthzObject("orders", "u2", 0), "read")).isFalse();

        principal = principal("u3", List.of("viewer", "auditor"));
        assertThat(engine.enforcer().enforce(
                new GuanjinAuthzSubject("u3", 0, Set.of("viewer", "auditor")), new GuanjinAuthzObject("orders", "u2", 0), "read")).isTrue();
        assertThat(engine.checkRow("u2")).isTrue();
    }

    @Test
    void labelLayer_clearance_gte_label() {
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
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
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
                .role("worker").can("orders", "read")
                .build();

        principal = principal("u1", List.of("worker"));
        assertThat(new GuanjinAuthzEngine(model, () -> principal).check("orders", "read")).isTrue();

        GuanjinAuthzModel withOwner = GuanjinAuthzModel.builder()
                .role("worker").can("orders", "read")
                .build();
        GuanjinAuthzEngine ownerEngine = new GuanjinAuthzEngine(withOwner, () -> principal);
        assertThat(ownerEngine.enforcer().enforce(
                new GuanjinAuthzSubject("u1", 0, Set.of("worker")), new GuanjinAuthzObject("orders", "u1", 0), "read")).isTrue();
        assertThat(ownerEngine.enforcer().enforce(
                new GuanjinAuthzSubject("u1", 0, Set.of("worker")), new GuanjinAuthzObject("orders", "u2", 0), "read")).isFalse();

        GuanjinAuthzModel withLabel = GuanjinAuthzModel.builder()
                .role("worker").withClearance(2).can("orders", "read")
                .dataLabel("orders", 2)
                .build();
        GuanjinAuthzEngine labelEngine = new GuanjinAuthzEngine(withLabel, () -> principal);
        assertThat(labelEngine.check("orders", "read")).isTrue();

        principal = principal("u1", List.of("worker"));
        GuanjinAuthzModel lowClearance = GuanjinAuthzModel.builder()
                .role("worker").withClearance(1).can("orders", "read")
                .dataLabel("orders", 2)
                .build();
        assertThat(new GuanjinAuthzEngine(lowClearance, () -> principal).check("orders", "read")).isFalse();
    }

    @Test
    void resourceGroup_expandsToContainedResources() {
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
                .role("order_admin").can("order_domain", "read")
                .resourceGroup("order_domain").contains("orders", "order_items")
                .build();
        engine = engine(model);
        principal = principal("u1", List.of("order_admin"));

        assertThat(engine.check("orders", "read")).isTrue();
        assertThat(engine.check("order_items", "read")).isTrue();
        assertThat(engine.check("payments", "read")).isFalse();
    }

    @Test
    void scope_unrestricted_for_writer_and_auditor() {
        GuanjinAuthzModel model = GuanjinAuthzModel.builder()
                .role("order_admin").can("orders", "read", "write")
                .role("order_viewer").can("orders", "read")
                .build();
        engine = engine(model);

        principal = principal("admin", List.of("order_admin"));
        assertThat(engine.scope("orders").isUnrestricted()).isTrue();

        principal = principal("viewer", List.of("order_viewer"));
        GuanjinScopeCondition restricted = engine.scope("orders");
        assertThat(restricted.isUnrestricted()).isFalse();
        assertThat(restricted.getOwnerUserId()).isEqualTo("viewer");

        principal = principal("auditor1", List.of("order_viewer", "auditor"));
        assertThat(engine.scope("orders").isUnrestricted()).isTrue();
    }
}
