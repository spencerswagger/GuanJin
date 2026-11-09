package com.guanjin.spring.boot2;

import com.guanjin.core.config.GuanjinConfig;
import com.guanjin.core.config.GuanjinConfigKeys;
import com.guanjin.core.config.ReplayStoreType;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

/**
 * AC-SDK-07：**反射扫描 `GuanjinProperties` ↔ 契约 §3.2 表逐项一致**（键名 + 类型 + 默认值），
 * 数量必须恰好 29、编号连续 1..29、不得有补号键。
 *
 * <p>这条测试直接解析 {@code docs/sdk-contract.md} 的表格，不复制一份"期望值"到代码里——
 * 否则契约改了而测试没改，就成了自证清白。
 */
class GuanjinPropertiesReflectionTest {

    private static final Charset UTF8 = Charset.forName("UTF-8");
    private static final Pattern CODE_SPAN = Pattern.compile("`([^`]+)`");

    @Test
    void AC_SDK_07_reflectionMatchesContractTableRowByRow() throws Exception {
        List<ContractRow> rows = parseContractRows();
        assertEquals(29, rows.size(), "契约 §3.2 必须恰好 29 行（D6-1）");

        GuanjinProperties properties = new GuanjinProperties();
        for (int i = 0; i < rows.size(); i++) {
            ContractRow row = rows.get(i);
            GuanjinConfigKeys.KeyDescriptor d = GuanjinConfigKeys.DESCRIPTORS.get(i);

            assertEquals(i + 1, row.index, "编号必须连续 1..29");
            assertEquals(row.index, d.index(), "描述符编号与契约不一致");
            assertEquals(row.key, d.key(), "键名与契约不一致（逐字符相同）");
            assertEquals(row.type, d.wireType(), "类型列与契约不一致：" + row.key);
            assertEquals(row.defaultText, d.contractDefault(), "默认值列与契约不一致：" + row.key);
            assertEquals(row.required, d.required(), "必填列与契约不一致：" + row.key);

            // 键能真的绑定到 GuanjinProperties（Java 8 列）
            Method getter = resolveGetter(properties, d.propertyPath());
            assertNotNull(getter, "契约里的 Java 路径无法在 GuanjinProperties 上解析：" + d.propertyPath());
            assertJavaTypeMatches(d, getter.getReturnType());

            Object value = getter.invoke(resolveParent(properties, d.propertyPath()));
            assertDefaultMatches(d, row, value);
        }

        // 反向：Java 侧不得有契约外的叶子属性（多一个键 = 私自扩契约）
        Set<String> javaLeaves = new TreeSet<String>();
        collectLeaves(GuanjinConfig.class, "", javaLeaves);
        Set<String> contractLeaves = new TreeSet<String>();
        for (GuanjinConfigKeys.KeyDescriptor d : GuanjinConfigKeys.DESCRIPTORS) {
            contractLeaves.add(d.propertyPath());
        }
        assertEquals(contractLeaves, javaLeaves, "GuanjinProperties 的叶子属性集必须与契约 29 项完全一致");
    }

    @Test
    void AC_SDK_01_reflectionShowsNoModeOrDirectProperty() throws Exception {
        Set<String> javaLeaves = new TreeSet<String>();
        collectLeaves(GuanjinConfig.class, "", javaLeaves);
        for (String leaf : javaLeaves) {
            assertFalse(leaf.equals("mode") || leaf.endsWith(".mode"), "不得存在 mode 属性：" + leaf);
            assertFalse(leaf.startsWith("direct."), "不得存在 direct 属性：" + leaf);
            assertFalse(leaf.contains("userToken"), "不得存在 userToken* 属性：" + leaf);
        }
        // 废止键不得能被绑定（ignoreUnknownFields=false 是绑定层的保证，见自动配置测试）
        for (String removed : GuanjinConfigKeys.REMOVED) {
            assertFalse(GuanjinConfigKeys.ALL.contains(removed));
        }
    }

    @Test
    void AC_SDK_07_derivedDefaultsResolveAsContractSays() {
        GuanjinProperties p = new GuanjinProperties();
        p.getPlatform().setBaseUrl("https://guanjin.example.com");
        p.getClient().setId("orders");
        assertNull(p.getGateway().getJwksUri(), "jwks-uri 默认不写死值，由 base-url 派生");
        assertNull(p.getGateway().getAud(), "aud 默认不写死值，跟随 client.id");
        assertEquals("https://guanjin.example.com/.well-known/jwks.json", p.resolvedJwksUri());
        assertEquals("orders", p.resolvedAud());
    }

    @Test
    void AC_SDK_10_defaultsAreRedisAndPullDisabled() {
        GuanjinProperties p = new GuanjinProperties();
        assertEquals(ReplayStoreType.REDIS, p.getAuthz().getReplayStore());
        assertFalse(p.getAgent().getPull().isEnabled());
    }

    // ---- 契约表格解析 ----

    private static List<ContractRow> parseContractRows() throws IOException {
        Path file = locateContract();
        List<String> lines = Files.readAllLines(file, UTF8);
        int start = -1;
        for (int i = 0; i < lines.size(); i++) {
            if (lines.get(i).startsWith("### 3.2")) {
                start = i;
                break;
            }
        }
        if (start < 0) {
            fail("在 " + file + " 里找不到 §3.2 配置表");
        }
        List<ContractRow> rows = new ArrayList<ContractRow>();
        for (int i = start + 1; i < lines.size(); i++) {
            String line = lines.get(i);
            if (!line.startsWith("|")) {
                if (rows.size() > 0) {
                    break; // 表格结束
                }
                continue;
            }
            String[] cells = line.split("\\|", -1);
            if (cells.length < 6) {
                continue;
            }
            String idx = cells[1].trim();
            if (!idx.matches("\\d+")) {
                continue; // 表头 / 分隔行
            }
            rows.add(new ContractRow(
                    Integer.parseInt(idx),
                    codeSpan(cells[2].trim()),
                    codeSpan(cells[3].trim()),
                    codeSpan(cells[4].trim()),
                    "是".equals(cells[5].replace("*", "").replace("`", "").trim())));
        }
        return rows;
    }

    private static String codeSpan(String cell) {
        Matcher m = CODE_SPAN.matcher(cell);
        if (m.find()) {
            return m.group(1).trim();
        }
        return cell.trim();
    }

    private static Path locateContract() {
        Path dir = Paths.get(System.getProperty("user.dir")).toAbsolutePath();
        while (dir != null) {
            Path candidate = dir.resolve("docs").resolve("sdk-contract.md");
            if (Files.isRegularFile(candidate)) {
                return candidate;
            }
            dir = dir.getParent();
        }
        throw new IllegalStateException("找不到 docs/sdk-contract.md（从 " + System.getProperty("user.dir") + " 向上找）");
    }

    // ---- 反射工具 ----

    private static void collectLeaves(Class<?> type, String prefix, Set<String> out) {
        for (Field f : type.getDeclaredFields()) {
            if (f.isSynthetic() || java.lang.reflect.Modifier.isStatic(f.getModifiers())) {
                continue;
            }
            String path = prefix.isEmpty() ? f.getName() : prefix + "." + f.getName();
            if (isNestedConfigType(f.getType())) {
                collectLeaves(f.getType(), path, out);
            } else {
                out.add(path);
            }
        }
    }

    /** 是否是 {@link GuanjinConfig} 的嵌套配置类（{@code GuanjinConfig$Agent$Report} 这类）。 */
    private static boolean isNestedConfigType(Class<?> type) {
        return type.isMemberClass() && type.getName().startsWith(GuanjinConfig.class.getName() + "$");
    }

    private static Method resolveGetter(Object root, String propertyPath) {
        Object parent = resolveParent(root, propertyPath);
        String last = lastSegment(propertyPath);
        String cap = Character.toUpperCase(last.charAt(0)) + last.substring(1);
        for (String name : new String[]{"get" + cap, "is" + cap}) {
            try {
                return parent.getClass().getMethod(name);
            } catch (NoSuchMethodException ignored) {
                // try next
            }
        }
        return null;
    }

    private static Object resolveParent(Object root, String propertyPath) {
        String[] segs = propertyPath.split("\\.");
        Object cur = root;
        for (int i = 0; i < segs.length - 1; i++) {
            cur = invokeGetter(cur, segs[i]);
        }
        return cur;
    }

    private static Object invokeGetter(Object target, String property) {
        String cap = Character.toUpperCase(property.charAt(0)) + property.substring(1);
        for (String name : new String[]{"get" + cap, "is" + cap}) {
            try {
                return target.getClass().getMethod(name).invoke(target);
            } catch (NoSuchMethodException ignored) {
                // try next
            } catch (Exception e) {
                throw new IllegalStateException("反射取值失败：" + property, e);
            }
        }
        throw new IllegalStateException("找不到 getter：" + property);
    }

    private static String lastSegment(String path) {
        int i = path.lastIndexOf('.');
        return i < 0 ? path : path.substring(i + 1);
    }

    private static void assertJavaTypeMatches(GuanjinConfigKeys.KeyDescriptor d, Class<?> javaType) {
        String t = d.wireType();
        if ("bool".equals(t)) {
            assertTrue(javaType == boolean.class || javaType == Boolean.class, d.key() + " 应为 boolean，实际 " + javaType);
        } else if ("duration".equals(t)) {
            assertEquals(Duration.class, javaType, d.key() + " 应为 Duration");
        } else if ("enum".equals(t)) {
            assertEquals(ReplayStoreType.class, javaType, d.key() + " 应为 ReplayStoreType");
        } else if ("list".equals(t)) {
            assertTrue(List.class.isAssignableFrom(javaType), d.key() + " 应为 List");
        } else if ("string".equals(t) || "string(url)".equals(t)
                || "string(bean/符号名)".equals(t) || "resource".equals(t)) {
            assertEquals(String.class, javaType, d.key() + " 应为 String");
        } else {
            fail("契约出现了未登记的类型列：" + t + "（" + d.key() + "）");
        }
    }

    private static void assertDefaultMatches(GuanjinConfigKeys.KeyDescriptor d, ContractRow row, Object value) {
        if ("—".equals(row.defaultText)) {
            // 契约里 "—" = 无默认值（必填项必是 "—"；非必填的 #27 也是 "—"：没配就没有）
            assertNull(value, d.key() + " 契约未给默认值，字段必须为空");
            return;
        }
        if (row.required) {
            assertNull(value, d.key() + " 是必填项，默认必须为空（禁硬编码）");
            return;
        }
        if (d.derivedDefault()) {
            assertNull(value, d.key() + " 的默认由其它键派生，字段本身必须为空");
            return;
        }
        String t = d.wireType();
        if ("bool".equals(t)) {
            assertEquals(Boolean.valueOf(row.defaultText), value, d.key() + " 默认值不一致");
        } else if ("duration".equals(t)) {
            assertEquals(parseDuration(row.defaultText), value, d.key() + " 默认值不一致");
        } else if ("enum".equals(t)) {
            assertEquals(ReplayStoreType.fromWire(row.defaultText), value, d.key() + " 默认值不一致");
        } else if ("list".equals(t)) {
            assertEquals(0, ((List<?>) value).size(), d.key() + " 默认应为空列表");
        } else {
            assertEquals(row.defaultText, value, d.key() + " 默认值不一致");
        }
    }

    private static Duration parseDuration(String text) {
        String v = text.trim().toLowerCase();
        if (v.endsWith("ms")) {
            return Duration.ofMillis(Long.parseLong(v.substring(0, v.length() - 2)));
        }
        long n = Long.parseLong(v.substring(0, v.length() - 1));
        char unit = v.charAt(v.length() - 1);
        if (unit == 's') {
            return Duration.ofSeconds(n);
        }
        if (unit == 'm') {
            return Duration.ofMinutes(n);
        }
        if (unit == 'h') {
            return Duration.ofHours(n);
        }
        if (unit == 'd') {
            return Duration.ofDays(n);
        }
        throw new IllegalArgumentException("无法解析 duration：" + text);
    }

    /** 契约 §3.2 的一行。 */
    private static final class ContractRow {
        final int index;
        final String key;
        final String type;
        final String defaultText;
        final boolean required;

        ContractRow(int index, String key, String type, String defaultText, boolean required) {
            this.index = index;
            this.key = key;
            this.type = type;
            this.defaultText = defaultText;
            this.required = required;
        }
    }
}
