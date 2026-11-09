package com.guanjin.core.config;

import java.util.Collection;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.Set;

/**
 * 运行环境标识（= Java 侧的 {@code spring.profiles.active}）。
 *
 * <p>契约 C-1 / C-4 的 profile 门控口径：**"多副本"= 非 {@code local|test} 的任何环境**，
 * 且**不新增配置键**表达部署形态（AC-SESS-05 明写）。因此这里只有一件事：
 * 判断当前 profile 里是否含 {@code local} 或 {@code test}。
 *
 * <p>失败方向是"判严"：**空集合 = 生产口径**（不是"本地"）。
 */
public final class GuanjinEnvironment {

    /** 允许关闭/降级的环境名（契约 C-1：仅 local|test）。 */
    public static final String PROFILE_LOCAL = "local";
    public static final String PROFILE_TEST = "test";

    private final Set<String> activeProfiles;

    private GuanjinEnvironment(Set<String> activeProfiles) {
        this.activeProfiles = Collections.unmodifiableSet(activeProfiles);
    }

    /** 从 Spring 的 active profiles 构造；{@code null} 视作空（= 生产口径）。 */
    public static GuanjinEnvironment of(Collection<String> activeProfiles) {
        Set<String> set = new LinkedHashSet<String>();
        if (activeProfiles != null) {
            for (String p : activeProfiles) {
                if (p == null) {
                    continue;
                }
                for (String part : p.split(",")) {
                    String v = part.trim().toLowerCase();
                    if (!v.isEmpty()) {
                        set.add(v);
                    }
                }
            }
        }
        return new GuanjinEnvironment(set);
    }

    /** 非 Spring 入口用：显式给定 profile 名（可空）。 */
    public static GuanjinEnvironment of(String... activeProfiles) {
        Set<String> set = new LinkedHashSet<String>();
        if (activeProfiles != null) {
            for (String p : activeProfiles) {
                if (p != null) {
                    set.add(p);
                }
            }
        }
        return of(set);
    }

    /** 生产口径（空 profile 集合）。 */
    public static GuanjinEnvironment production() {
        return of(Collections.<String>emptySet());
    }

    /** 是否处于允许"关掉 / 用 memory"的环境（C-1 / C-4 / AC-SDK-02 / AC-SESS-05）。 */
    public boolean isLocalOrTest() {
        return activeProfiles.contains(PROFILE_LOCAL) || activeProfiles.contains(PROFILE_TEST);
    }

    public Set<String> activeProfiles() {
        return activeProfiles;
    }

    @Override
    public String toString() {
        return activeProfiles.isEmpty() ? "(none => prod)" : activeProfiles.toString();
    }
}
