package com.guanjin.core.authz;

import com.guanjin.core.error.GuanjinSdkException;
import com.guanjin.core.principal.GuanjinContextHolder;
import com.guanjin.core.principal.GuanjinPrincipal;
import org.casbin.jcasbin.main.Enforcer;
import org.casbin.jcasbin.model.Model;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;

/**
 * 授权判定门面（契约 GJ-CAP-12 {@code Authz}）：**jcasbin + 真 {@code model.conf} 文件**。
 *
 * <p>三层能力的分工（基线《权限模型设计》§3.4）：
 * <ul>
 *   <li>RBAC（角色 → 资源 → 动作）在本类的 Casbin enforcer 里判，模型文本来自
 *       {@code guanjin.authz.model-conf}（默认 {@code classpath:/guanjin/model.conf}），
 *       策略来自业务方声明的 {@link AuthzModel}——**都不写在代码里**；</li>
 *   <li>行级（GJ-CAP-15 {@code checkRow}）与密级（GJ-CAP-16 {@code checkLabel}）是**独立入口**，
 *       由业务在调用点显式调用（基线 §2.1 的示例代码形态）；</li>
 *   <li>失败一律 403 {@code INSUFFICIENT_ROLE}（契约 §4.4：拒绝授权判定），不放行。</li>
 * </ul>
 *
 * <p>没有 {@code init()} 就调用判定 → 直接抛错（**不是"没配就放行"**）。
 */
public final class Authz {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final String CLASSPATH_PREFIX = "classpath:";

    /** auditor 角色自动获得行级豁免（基线 §2.1 明确写死，不是可配置项）。 */
    public static final String AUDITOR_ROLE = "auditor";

    private static volatile Enforcer enforcer;
    private static volatile AuthzModel model;
    private static volatile String modelConfLocation;

    private Authz() {
    }

    /**
     * 装载模型与策略（启动期调用一次）。
     *
     * @param declaredModel     业务方声明的角色/资源/动作
     * @param modelConfLocation {@code classpath:/guanjin/model.conf} 或文件路径（配置 #17）
     */
    public static synchronized void init(AuthzModel declaredModel, String modelConfLocation) {
        if (declaredModel == null) {
            throw new IllegalStateException("AuthzModel 不能为 null（GJ-CAP-13）");
        }
        String text = readModelConf(modelConfLocation);
        Model casbinModel = new Model();
        casbinModel.loadModelFromText(text);
        Enforcer e = new Enforcer(casbinModel);
        for (String line : declaredModel.toPolicyLines()) {
            List<String> parts = split(line);
            if (parts.isEmpty()) {
                continue;
            }
            String ptype = parts.get(0);
            List<String> args = parts.subList(1, parts.size());
            if ("p".equals(ptype)) {
                e.addPolicy(args);
            } else if ("g".equals(ptype) || "g2".equals(ptype)) {
                e.addNamedGroupingPolicy(ptype, args);
            } else {
                throw new IllegalStateException("model.conf 里的策略类型只支持 p/g/g2，得到 " + ptype);
            }
        }
        enforcer = e;
        model = declaredModel;
        Authz.modelConfLocation = modelConfLocation;
    }

    public static boolean isInitialized() {
        return enforcer != null;
    }

    /** 生效的模型（排障/上报用）。 */
    public static AuthzModel model() {
        return model;
    }

    /** 生效的 model.conf 位置（配置 #17 的解析结果）。 */
    public static String modelConfLocation() {
        return modelConfLocation;
    }

    /** GJ-CAP-12：主体对资源/动作是否放行（RBAC 层，走 Casbin）。 */
    public static boolean enforce(GuanjinPrincipal principal, String resource, String action) {
        Enforcer e = requireEnforcer();
        if (principal == null) {
            return false;
        }
        if (resource == null || resource.trim().isEmpty() || action == null || action.trim().isEmpty()) {
            throw new IllegalArgumentException("resource/action 不能为空（不做\"空即放行\"的推断）");
        }
        for (String role : effectiveRoles(principal)) {
            if (e.enforce(role, resource, action)) {
                return true;
            }
        }
        return false;
    }

    /** GJ-CAP-14：不放行即 403 {@code INSUFFICIENT_ROLE}（fail-closed）。 */
    public static void require(GuanjinPrincipal principal, String resource, String action) {
        if (principal == null) {
            throw GuanjinSdkException.tokenInvalid("授权判定缺少身份（未认证）");
        }
        if (!enforce(principal, resource, action)) {
            throw GuanjinSdkException.insufficientRole(
                    "权限不足：resource=" + resource + " action=" + action
                            + " roles=" + effectiveRoles(principal));
        }
    }

    /**
     * GJ-CAP-15 行级判定：只能访问自己的数据行；{@code auditor} 角色豁免（基线写死）。
     *
     * @param ownerId 数据行的属主（通常是 {@code sub}）
     */
    public static void checkRow(String ownerId) {
        GuanjinPrincipal p = GuanjinContextHolder.current();
        if (p == null) {
            throw GuanjinSdkException.tokenInvalid("行级判定缺少身份（未认证）");
        }
        if (ownerId == null || ownerId.trim().isEmpty()) {
            throw GuanjinSdkException.insufficientRole("行级判定缺少属主（ownerId 为空）");
        }
        if (effectiveRoles(p).contains(AUDITOR_ROLE)) {
            return;
        }
        if (!ownerId.equals(p.subject())) {
            throw GuanjinSdkException.insufficientRole(
                    "行级拒绝：该数据行不属于当前主体（owner=" + ownerId + "）");
        }
    }

    /**
     * GJ-CAP-16 密级判定：主体密级 ≥ 客体密级（形参名固定 {@code objLevel}）。
     *
     * <p>公开数据不占级（{@code objLevel <= 0} 直接放行）。
     */
    public static void checkLabel(int objLevel) {
        GuanjinPrincipal p = GuanjinContextHolder.current();
        if (p == null) {
            throw GuanjinSdkException.tokenInvalid("密级判定缺少身份（未认证）");
        }
        if (objLevel <= 0) {
            return;
        }
        int clearance = clearanceOf(p);
        if (clearance < objLevel) {
            throw GuanjinSdkException.insufficientRole(
                    "密级不足：主体=" + clearance + " 客体=" + objLevel);
        }
    }

    /** 主体密级 = 其角色里声明的最高密级（未声明 = 0）。 */
    public static int clearanceOf(GuanjinPrincipal principal) {
        AuthzModel m = model;
        if (m == null || principal == null) {
            return 0;
        }
        int max = 0;
        for (String role : effectiveRoles(principal)) {
            int v = m.clearanceOf(role);
            if (v > max) {
                max = v;
            }
        }
        return max;
    }

    /** 生效角色：身份里没绑角色时用 {@code defaultRole}（GJ-CAP-18 兜底，权限最小）。 */
    public static List<String> effectiveRoles(GuanjinPrincipal principal) {
        if (principal != null && !principal.roles().isEmpty()) {
            return principal.roles();
        }
        AuthzModel m = model;
        if (m == null || m.defaultRole() == null) {
            return Collections.emptyList();
        }
        return Collections.singletonList(m.defaultRole());
    }

    private static Enforcer requireEnforcer() {
        Enforcer e = enforcer;
        if (e == null) {
            throw new IllegalStateException(
                    "Authz 未初始化：必须先 Authz.init(model, modelConf)（不得\"没配就放行\"）");
        }
        return e;
    }

    private static String readModelConf(String location) {
        if (location == null || location.trim().isEmpty()) {
            throw new IllegalStateException("authz.model-conf 为空（配置 #17）");
        }
        String loc = location.trim();
        if (loc.startsWith(CLASSPATH_PREFIX)) {
            String path = loc.substring(CLASSPATH_PREFIX.length());
            while (path.startsWith("/")) {
                path = path.substring(1);
            }
            InputStream in = Authz.class.getClassLoader().getResourceAsStream(path);
            if (in == null) {
                throw new IllegalStateException("classpath 上找不到 model.conf：" + loc);
            }
            try {
                return new String(readFully(in), UTF8);
            } catch (IOException e) {
                throw new IllegalStateException("读取 model.conf 失败：" + loc, e);
            } finally {
                try {
                    in.close();
                } catch (IOException ignored) {
                    // 关闭失败不影响判定
                }
            }
        }
        try {
            return new String(Files.readAllBytes(new File(loc).toPath()), UTF8);
        } catch (IOException e) {
            throw new IllegalStateException("读取 model.conf 失败：" + loc, e);
        }
    }

    private static byte[] readFully(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream(4096);
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) >= 0) {
            out.write(buf, 0, n);
        }
        return out.toByteArray();
    }

    private static List<String> split(String line) {
        List<String> out = new ArrayList<String>();
        for (String part : Arrays.asList(line.split(","))) {
            String v = part.trim();
            if (!v.isEmpty()) {
                out.add(v);
            }
        }
        return out;
    }
}
