package com.guanjin.core.config;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;

/** 测试夹具：一份**合法**的最小配置（必填项齐全、默认值原样）。 */
public final class TestConfigs {

    public static final String BASE_URL = "http://127.0.0.1:18080";

    private TestConfigs() {
    }

    /** 合法最小配置：只补必填项，其余保持契约默认值。 */
    public static GuanjinConfig minimal() {
        GuanjinConfig cfg = new GuanjinConfig();
        cfg.getPlatform().setBaseUrl(BASE_URL);
        cfg.getClient().setId("orders");
        cfg.getClient().setHmacSecret("s3cr3t-not-in-repo");
        return cfg;
    }

    /** 契约默认值里 `memory` 被禁的环境：本地/测试。 */
    public static GuanjinEnvironment local() {
        return GuanjinEnvironment.of("local");
    }

    /** 生产口径（空 profile = 判严）。 */
    public static GuanjinEnvironment prod() {
        return GuanjinEnvironment.production();
    }

    public static java.util.List<String> list(String... v) {
        return new ArrayList<String>(Arrays.asList(v));
    }

    public static Duration secs(long s) {
        return Duration.ofSeconds(s);
    }
}
