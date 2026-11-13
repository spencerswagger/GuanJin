package com.guanjin.core.startup;

import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.GuanjinConfigValidator;
import com.guanjin.core.config.GuanjinEnvironment;
import com.guanjin.core.jwks.JwksClient;
import com.guanjin.core.jwks.JwksSet;
import com.guanjin.core.publicpath.PathMatcher;

import java.util.ArrayList;
import java.util.List;

/**
 * 启动自检（契约 GJ-CAP-22）：**校验必填项 → 拉 JWKS → 打印放行清单**，失败即启动失败。
 *
 * <p>关键判据（AC-SDK-03 / AC-SDK-04）：
 * <ul>
 *   <li>默认 29 项配置必须**首启即命中真实 JWKS**；404/500 **即失败**，不许降级成警告；</li>
 *   <li>平台地址缺失或非法 → **启动失败**（不是打印后继续跑、也不是退化为匿名）；</li>
 *   <li>放行清单必须可审计（只打路径，不打令牌/secret）。</li>
 * </ul>
 */
public final class GuanjinStartupChecker {

    private GuanjinStartupChecker() {
    }

    /**
     * 跑一次启动自检。
     *
     * @throws com.guanjin.core.config.GuanjinConfigException 配置不合法（启动失败）
     * @throws com.guanjin.core.error.GuanjinSdkException     JWKS 不可得（503 KEY_UNAVAILABLE，启动失败）
     */
    public static StartupReport check(GuanjinConfig cfg, GuanjinEnvironment env, JwksClient jwks) {
        List<String> warnings = new ArrayList<String>(GuanjinConfigValidator.validate(cfg, env));
        if (jwks == null) {
            throw new IllegalStateException("启动自检必须提供 JwksClient（GJ-CAP-03：启动期唯一拉取点）");
        }
        // 启动期唯一拉取点：拉不到即启动失败（404 也是失败，不是"先跑起来再说"）
        JwksSet set = jwks.refresh();
        PathMatcher matcher = new PathMatcher(cfg.getPublicPaths(), cfg.isPublicPathsIncludeBuiltin());
        List<String> gatewayKids = new ArrayList<String>();
        for (com.guanjin.core.jwks.JwksKey k : set.gatewayKeys()) {
            gatewayKids.add(k.kid());
        }
        if (gatewayKids.isEmpty()) {
            warnings.add("JWKS 里没有 alg=" + com.guanjin.core.jwks.JwksKey.ALG_SM2
                    + " 的网关用途公钥：任何断言都会 401（检查平台 signing_keys 的 purpose=gateway）");
        }
        return new StartupReport(cfg.getPlatform().getBaseUrl(), jwks.jwksUri().toString(),
                set.kids(), gatewayKids, matcher.patterns(), warnings);
    }
}
