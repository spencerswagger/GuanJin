package com.guanjin.core.assertion;

import com.guanjin.core.error.GuanjinSdkException;

/**
 * 断言验签 SPI（契约 GJ-CAP-01：{@code AssertionVerifier.verify(raw)}，**无跳过开关**）。
 *
 * <p>本切片（S9）只交付"验签之后的链路"：claim 校验、{@code jti} 防重放、剥离、上下文注入。
 * SM2 验签实现（BouncyCastle + {@code kid} 选键 + {@code alg} 固定 {@code SM2}（D29））在后续切片
 * 提供 {@code Sm2AssertionVerifier}；**在那之前，宿主拿不到验签器 bean 时启动会失败**，
 * 不会出现"没验签却开始放行"的形态（见 {@code guanjin-spring-boot2} 的启动守卫）。
 *
 * <p>实现方**不得**返回 {@code null}、也不得在失败时返回"匿名身份"：失败一律抛
 * 401 {@code TOKEN_INVALID}。
 */
public interface AssertionVerifier {

    /**
     * 验证签名并解析 claim。
     *
     * @param rawAssertion 原始断言串（JWS compact）
     * @return 已验签的断言
     * @throws GuanjinSdkException 401 {@code TOKEN_INVALID}（签名错/结构坏/alg 不合法）
     */
    Assertion verify(String rawAssertion);
}
