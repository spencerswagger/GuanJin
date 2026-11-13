package com.guanjin.spring.boot2;

import com.guanjin.core.config.GuanjinConfigException;
import com.guanjin.core.config.GuanjinConfigValidator;
import org.springframework.beans.BeansException;
import org.springframework.beans.factory.config.BeanFactoryPostProcessor;
import org.springframework.beans.factory.config.ConfigurableListableBeanFactory;
import org.springframework.boot.context.properties.source.ConfigurationProperty;
import org.springframework.boot.context.properties.source.ConfigurationPropertyName;
import org.springframework.boot.context.properties.source.ConfigurationPropertySource;
import org.springframework.boot.context.properties.source.ConfigurationPropertySources;
import org.springframework.boot.origin.OriginTrackedValue;
import org.springframework.context.EnvironmentAware;
import org.springframework.core.Ordered;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.Environment;
import org.springframework.core.env.MapPropertySource;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 时长键的**绑定期守卫**（契约 §3.2.1，`FROZEN`；判据 `AC-SDK-11`）。
 *
 * <p><b>为什么需要它</b>：Spring 的 {@code Duration} 默认转换器接受纯数字（按**毫秒**）、
 * {@code d}（按**天**）和 ISO-8601 {@code PT60S}——同一份配置「Java 起得来、Go 拒」= 静默不兼容。
 * 契约裁决是**收紧 Java、不放宽契约**：合法性**只能**由 {@link GuanjinConfigValidator#parseDuration}
 * 的 §3.2.1 正则判定，不得交给默认转换器。
 *
 * <p><b>为什么是 {@link BeanFactoryPostProcessor}</b>：这是"绑定之前"的最后一个钩子——
 * {@code ConfigurationPropertiesBindingPostProcessor} 是 BeanPostProcessor，在 Bean 实例化时才绑定，
 * 而本守卫在所有 Bean 实例化**之前**跑，因此在非法文本被转换成 {@code Duration} **之前**就能挡住
 * （一旦绑完，{@code 3000} 与 {@code 3000ms} 在类型上无法区分）。
 *
 * <p><b>为什么按键名逐个查、而不是遍历所有属性</b>：Boot 2.7 的
 * {@link ConfigurationPropertySource} **不可枚举**（只有 {@code getConfigurationProperty(name)}），
 * 而键名单本来就在契约里（{@link GuanjinConfigValidator#durationKeys()}，取自
 * {@code GuanjinConfigKeys.DESCRIPTORS} 的类型列，**不硬编码键名**）。查找用
 * {@link ConfigurationPropertyName} 走 Spring 的宽松绑定映射，因此规范 kebab 名、
 * 宽松 camelCase 名（{@code guanjin.platform.connectTimeout}）、环境变量名
 * （{@code GUANJIN_PLATFORM_CONNECT_TIMEOUT}）都能命中。属性源按优先级从高到低迭代，
 * **每个键只看生效的那份**（取第一个命中即停），避免"被覆盖的低优先级历史值反而让进程起不来"。
 *
 * <p>失败行为与 §3.2.1 / §3.6 同口径：抛 {@link GuanjinConfigException}（**含键名**）→ 启动失败，
 * **不回落默认值**、不告警后继续。占位符（{@code ${...}}）先按环境解析再判——解析不掉即失败，
 * 不允许"看不懂就放过"。
 *
 * <p>零 Spring 的判定逻辑全在 core（{@link GuanjinConfigValidator#parseDuration}）；本类只负责
 * "从 Spring 环境里把用户写的原始文本找出来"。
 */
final class GuanjinDurationBindingGuard implements BeanFactoryPostProcessor, EnvironmentAware, Ordered {

    private ConfigurableEnvironment environment;

    @Override
    public void setEnvironment(Environment environment) {
        this.environment = environment instanceof ConfigurableEnvironment
                ? (ConfigurableEnvironment) environment : null;
    }

    @Override
    public void postProcessBeanFactory(ConfigurableListableBeanFactory beanFactory) throws BeansException {
        if (environment == null) {
            // 非 ConfigurableEnvironment（极少见）：绑定期的失败仍由 GuanjinConfigValidator 兜住
            return;
        }
        Iterable<ConfigurationPropertySource> sources = ConfigurationPropertySources.get(environment);
        Map<String, Object> normalized = new LinkedHashMap<String, Object>();
        for (String key : GuanjinConfigValidator.durationKeys()) {
            ConfigurationProperty property = findEffective(sources, key);
            if (property == null) {
                continue;
            }
            Object raw = unwrap(property.getValue());
            String canonical = requireDurationText(key, raw);
            if (canonical != null && !canonical.equals(String.valueOf(raw))) {
                normalized.put(key, canonical);
            }
        }
        if (!normalized.isEmpty()) {
            // 契约 §3.2.1 显式允许「首尾空白」（trim 是写进契约的），但 Spring 的 Duration 转换器
            // **不做 trim**（" 2s " 会抛 ConversionFailedException，且文案不带 §3.2.1）。
            // 判过之后把 trim 版本以最高优先级塞回去，binder 拿到的就是规范文本——
            // 否则同一条契约在 Java 主路径上做不到（Go 侧本来就会先 trim 再 ParseDuration）。
            // trim 字符集 = 契约冻结的 4 个 ASCII 空白（GuanjinConfigValidator.trimAsciiWhitespace），
            // 不是 String.trim()：后者会多剥 U+0000–U+001F，与 Go 侧口径分叉。
            environment.getPropertySources()
                    .addFirst(new MapPropertySource("guanjinDurationTrimmed", normalized));
        }
    }

    /** 取该键**生效**的那份原始属性（属性源已按优先级排序，取第一个命中即停）。 */
    private static ConfigurationProperty findEffective(Iterable<ConfigurationPropertySource> sources, String key) {
        ConfigurationPropertyName name = ConfigurationPropertyName.of(key);
        for (ConfigurationPropertySource source : sources) {
            ConfigurationProperty property = source.getConfigurationProperty(name);
            if (property != null) {
                return property;
            }
        }
        return null;
    }

    /**
     * 生效值不是 §3.2.1 的字符串形态（或占位符解析不掉）→ 启动失败，报键名。
     *
     * @return 通过校验后的**规范文本**（占位符已解析、首尾空白已 trim），供调用方回写属性源
     */
    private String requireDurationText(String key, Object value) {
        if (value == null) {
            throw new GuanjinConfigException(key,
                    "取不到时长（契约 §3.2.1：必须显式给出 " + GuanjinConfigValidator.DURATION_PATTERN + " 形态，如 2s）。"
                            + "空值不按默认值处理（禁止静默回落）");
        }
        if (!(value instanceof CharSequence)) {
            // YAML 的 `connect-timeout: 3000` 会给出 Integer/Long；裸数字在 Java 侧曾被当作毫秒，
            // 那正是 §3.2.1 要消灭的隐式单位
            throw new GuanjinConfigException(key, "必须是字符串形态 <非负整数><单位>（契约 §3.2.1："
                    + GuanjinConfigValidator.DURATION_PATTERN + "，单位 ms|s|m|h）；得到非字符串值 \""
                    + value + "\"（" + value.getClass().getSimpleName() + "）——裸数字不按毫秒解释");
        }
        String text = value.toString();
        if (text.contains("${")) {
            text = environment.resolvePlaceholders(text);
            if (text.contains("${")) {
                throw new GuanjinConfigException(key,
                        "占位符无法解析成时长（契约 §3.2.1 不允许放过看不懂的值），原文 \"" + value + "\"");
            }
        }
        GuanjinConfigValidator.parseDuration(key, text);
        return GuanjinConfigValidator.trimAsciiWhitespace(text);
    }

    private static Object unwrap(Object value) {
        return value instanceof OriginTrackedValue ? ((OriginTrackedValue) value).getValue() : value;
    }

    /**
     * 必须**早**跑：BFPP 之间互不干扰，这里取最低优先级以免抢在占位符/属性源装配之前
     * （本类只读环境，不改 Bean 定义）。
     */
    @Override
    public int getOrder() {
        return Ordered.LOWEST_PRECEDENCE;
    }
}
