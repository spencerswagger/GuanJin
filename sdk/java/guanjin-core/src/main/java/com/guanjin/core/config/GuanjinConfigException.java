package com.guanjin.core.config;

/**
 * 配置错误：**一律启动失败**，且必须报出键名（契约 §3.6：不允许只说"配置错误"）。
 *
 * <p>不是 {@code RuntimeException} 的随机包装——{@link #getKey()} 就是那个键（或键族，
 * 例如未知键会一次列出全部），调用方（Spring 适配层 / 非 Spring 入口）拿它决定进程非 0 退出。
 */
public class GuanjinConfigException extends RuntimeException {

    private static final long serialVersionUID = 1L;

    /** 出问题的配置键；未知键/废止键场景为命中的那个键。 */
    private final String key;

    public GuanjinConfigException(String key, String reason) {
        super("guanjin: config: 键 \"" + key + "\" " + reason);
        this.key = key;
    }

    public GuanjinConfigException(String key, String reason, Throwable cause) {
        super("guanjin: config: 键 \"" + key + "\" " + reason, cause);
        this.key = key;
    }

    public String getKey() {
        return key;
    }
}
