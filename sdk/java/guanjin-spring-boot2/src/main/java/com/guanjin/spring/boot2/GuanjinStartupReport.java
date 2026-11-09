package com.guanjin.spring.boot2;

import com.guanjin.core.startup.StartupReport;

/**
 * 启动自检结果的容器 bean（GJ-CAP-22）：它的**构造即执行**自检，失败就启动失败。
 *
 * <p>单独做成 bean 的原因：自检必须在容器 refresh 期间跑完（早于任何请求），
 * 且宿主/运维需要能读到"这次到底验了哪几把键、放行了哪些路径"。
 */
public class GuanjinStartupReport {

    private final StartupReport report;

    public GuanjinStartupReport(StartupReport report) {
        this.report = report;
    }

    public StartupReport report() {
        return report;
    }
}
