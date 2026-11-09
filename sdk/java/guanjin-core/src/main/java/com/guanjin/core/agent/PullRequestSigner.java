package com.guanjin.core.agent;

import java.util.Map;

/**
 * 策略拉取请求的签名器（GJ-CAP-21 的 Agent 面 HMAC-SM3）。
 *
 * <p>为什么单独抽 SPI：**SDK 不得发出未签名的回源请求**。HMAC canonical 串（契约 §4.3）属于
 * Agent 面切片的交付物；在那之前 {@code agent.pull.enabled=true} 而没有本 SPI 的实现时，
 * 启动必须失败（{@code guanjin-spring-boot2} 的守卫），而不是"先发个裸请求试试"。
 */
public interface PullRequestSigner {

    /**
     * 产出附加到拉取请求上的头（含 HMAC 签名与客户端标识）。
     *
     * @param method HTTP 方法（固定 {@code GET}）
     * @param path   冻结端点路径（{@code /api/v1/agent/policies}）
     */
    Map<String, String> headers(String method, String path);
}
