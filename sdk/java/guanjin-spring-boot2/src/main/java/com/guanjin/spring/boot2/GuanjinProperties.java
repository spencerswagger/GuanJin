package com.guanjin.spring.boot2;

import com.guanjin.core.config.GuanjinConfig;
import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Spring 侧的 29 项配置绑定（契约 §3.2 的 Java 8 列）。
 *
 * <p>两条关键设定（契约 §3.6 的 Java 第 ① 条）：
 * <ul>
 *   <li>{@code ignoreUnknownFields = false} —— **Spring Boot 2.x 默认是 true**，不显式置 false 的话
 *       未知键（含拼错的 {@code guanjin.jawkes-uri}、已废止的 {@code guanjin.mode}）会被静默吞掉；</li>
 *   <li>字段与默认值全部继承自 {@link GuanjinConfig}（core，零 Spring），
 *       所以"Spring 绑定"与"非 Spring 装载"口径只有一份。</li>
 * </ul>
 */
@ConfigurationProperties(prefix = "guanjin", ignoreUnknownFields = false)
public class GuanjinProperties extends GuanjinConfig {
}
