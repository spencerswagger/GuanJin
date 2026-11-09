package com.guanjin.core.authz;

import com.guanjin.core.error.ErrorCodes;
import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Arrays;
import java.util.Collections;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * GJ-CAP-12~18：**真 jcasbin + 真 model.conf 文件**（classpath:/guanjin/model.conf），
 * 策略来自业务声明的 {@link AuthzModel}（不内联硬编码）。
 */
class AuthzTest {

    @BeforeEach
    void init() {
        Authz.init(AuthzModel.builder()
                        .role("order_admin").can("orders", "read", "write")
                        .role("order_viewer").can("orders", "read")
                        .role("auditor").can("orders", "read").withClearance(3)
                        .role("manager").can("orders", "read").withClearance(3)
                        .defaultRole("user").can("profile", "read")
                        .resourceGroup("order-res").contains("orders")
                        .build(),
                "classpath:/guanjin/model.conf");
        GuanjinContextHolder.clear();
    }

    @Test
    void modelConfComesFromRealClasspathFile() {
        assertEquals("classpath:/guanjin/model.conf", Authz.modelConfLocation());
        assertTrue(Authz.isInitialized());
        // 文件真的被解析了：换成一个不存在的路径必须失败（不是"没文件也能跑"）
        assertThrows(IllegalStateException.class, () -> Authz.init(model(), "classpath:/guanjin/nope.conf"));
    }

    @Test
    void roleCanReadWriteButNotDelete() {
        GuanjinPrincipal admin = principal("order_admin");
        assertTrue(Authz.enforce(admin, "orders", "read"));
        assertTrue(Authz.enforce(admin, "orders", "write"));
        assertFalse(Authz.enforce(admin, "orders", "delete"), "未声明的动作一律拒（不做\"空即全部\"推断）");
        assertFalse(Authz.enforce(admin, "profile", "read"), "未声明的资源一律拒");
    }

    @Test
    void requireThrows403InsufficientRole() {
        GuanjinSdkException e = assertThrows(GuanjinSdkException.class,
                () -> Authz.require(principal("order_viewer"), "orders", "write"));
        assertEquals(403, e.getStatus());
        assertEquals(ErrorCodes.INSUFFICIENT_ROLE, e.getCode());
    }

    @Test
    void defaultRoleIsFallbackWithMinimalPermission() {
        GuanjinPrincipal noRoles = new GuanjinPrincipal("user-9", "bob",
                Collections.<String>emptyList(), "AAL2", Collections.<String>emptyList(), "orders", "jti-9");
        assertTrue(Authz.enforce(noRoles, "profile", "read"), "默认角色兜底：profile:read");
        assertFalse(Authz.enforce(noRoles, "orders", "read"), "默认角色权限最小，不得\"已登录即全部\"");
    }

    @Test
    void resourceGroupMakesGroupPolicyMatchMemberResources() {
        Authz.init(AuthzModel.builder()
                        .role("order_admin").can("order-res", "read")
                        .defaultRole("user").can("profile", "read")
                        .resourceGroup("order-res").contains("orders").contains("order_items")
                        .build(),
                "classpath:/guanjin/model.conf");
        assertTrue(Authz.enforce(principal("order_admin"), "orders", "read"), "资源组成员应命中组策略");
        assertTrue(Authz.enforce(principal("order_admin"), "order_items", "read"));
    }

    @Test
    void checkRowAllowsOwnerAndExemptsAuditor() {
        GuanjinContextHolder.set(principal("order_viewer"));
        Authz.checkRow("user-1"); // 自己的行
        assertEquals(ErrorCodes.INSUFFICIENT_ROLE, assertThrows(GuanjinSdkException.class,
                () -> Authz.checkRow("user-2")).getCode());

        GuanjinContextHolder.set(new GuanjinPrincipal("user-1", "alice",
                Arrays.asList("auditor"), "AAL2", Arrays.asList("pwd"), "orders", "jti-a"));
        Authz.checkRow("user-2"); // auditor 豁免（基线写死）
    }

    @Test
    void checkLabelComparesClearanceToObjLevel() {
        GuanjinContextHolder.set(new GuanjinPrincipal("user-1", "alice",
                Arrays.asList("manager"), "AAL2", Arrays.asList("pwd"), "orders", "jti-m"));
        Authz.checkLabel(1);
        Authz.checkLabel(3);
        assertEquals(ErrorCodes.INSUFFICIENT_ROLE, assertThrows(GuanjinSdkException.class,
                () -> Authz.checkLabel(4)).getCode());
        // 公开数据不占级
        GuanjinContextHolder.set(principal("order_viewer"));
        Authz.checkLabel(0);
    }

    @Test
    void unauthenticatedChecksAreRejected() {
        GuanjinContextHolder.clear();
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> Authz.checkRow("user-1")).getCode());
        assertEquals(ErrorCodes.TOKEN_INVALID, assertThrows(GuanjinSdkException.class,
                () -> Authz.require(null, "orders", "read")).getCode());
    }

    private static AuthzModel model() {
        return AuthzModel.builder().defaultRole("user").can("profile", "read").build();
    }

    private static GuanjinPrincipal principal(String role) {
        return new GuanjinPrincipal("user-1", "alice", Arrays.asList(role), "AAL2",
                Arrays.asList("pwd"), "orders", "jti-" + role);
    }
}
