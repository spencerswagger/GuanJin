package com.guanjin.spring.boot2;

import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletRequestWrapper;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Enumeration;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * **替换请求视图**（契约 D10 第 ⑤ 步 / GJ-CAP-08 {@code HeaderStrippingRequestWrapper}）。
 *
 * <p>不是"删 header"，而是交给宿主一个新的视图：{@code getHeader} 返回 {@code null}、
 * {@code getHeaders} 返回空枚举、{@code getHeaderNames} 不含该名。这样 handler、下游转发、
 * 日志三类消费者**都取不到**原始断言（AC-SDK-08 的三处 consumer 断言）。
 *
 * <p>匹配**大小写不敏感**（HTTP header 语义；配置 #7 也要求大小写不敏感）。
 */
public class HeaderStrippingRequestWrapper extends HttpServletRequestWrapper {

    private final String strippedHeader;
    private final Set<String> strippedNames;

    public HeaderStrippingRequestWrapper(HttpServletRequest request, String headerName) {
        super(request);
        this.strippedHeader = headerName;
        Set<String> names = new LinkedHashSet<String>();
        names.add(headerName.toLowerCase());
        this.strippedNames = Collections.unmodifiableSet(names);
    }

    @Override
    public String getHeader(String name) {
        if (isStripped(name)) {
            return null;
        }
        return super.getHeader(name);
    }

    @Override
    public Enumeration<String> getHeaders(String name) {
        if (isStripped(name)) {
            return Collections.enumeration(Collections.<String>emptyList());
        }
        return super.getHeaders(name);
    }

    @Override
    public Enumeration<String> getHeaderNames() {
        Enumeration<String> original = super.getHeaderNames();
        if (original == null) {
            return null;
        }
        List<String> kept = new ArrayList<String>();
        while (original.hasMoreElements()) {
            String n = original.nextElement();
            if (!isStripped(n)) {
                kept.add(n);
            }
        }
        return Collections.enumeration(kept);
    }

    @Override
    public int getIntHeader(String name) {
        if (isStripped(name)) {
            return -1;
        }
        return super.getIntHeader(name);
    }

    @Override
    public long getDateHeader(String name) {
        if (isStripped(name)) {
            return -1L;
        }
        return super.getDateHeader(name);
    }

    private boolean isStripped(String name) {
        return name != null && strippedNames.contains(name.toLowerCase());
    }

    /** 被剥离的头名（排障用；不含值）。 */
    public String strippedHeader() {
        return strippedHeader;
    }
}
