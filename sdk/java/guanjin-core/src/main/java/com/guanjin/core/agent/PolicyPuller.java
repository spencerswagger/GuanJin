package com.guanjin.core.agent;

import com.guanjin.core.http.HttpFetchException;
import com.guanjin.core.http.HttpFetcher;
import com.guanjin.core.http.HttpResult;

import java.net.URI;
import java.time.Duration;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

/**
 * 策略拉取（契约 GJ-CAP-21 {@code PolicyPull}）：按需 {@code GET /api/v1/agent/policies}。
 *
 * <p>默认**不启用**（{@code guanjin.agent.pull.enabled=false}，契约 ✅C-5 / AC-SDK-10①）：
 * 方案 A 是"角色放进令牌、零回源"，默认打开等于默认给平台加负载。
 *
 * <p>两条硬约束：
 * <ul>
 *   <li>**不得发未签名请求**：没有 {@link PullRequestSigner} 就构造失败；</li>
 *   <li>拉取失败**不阻塞业务请求**（契约 §4.4：判定不依赖平台）——这里只记次数，不抛给业务。</li>
 * </ul>
 */
public final class PolicyPuller implements AutoCloseable {

    private final URI policiesUri;
    private final Duration interval;
    private final HttpFetcher fetcher;
    private final PullRequestSigner signer;

    private final AtomicLong attempts = new AtomicLong();
    private final AtomicLong successes = new AtomicLong();
    private final AtomicInteger lastStatus = new AtomicInteger(-1);

    private volatile ScheduledExecutorService scheduler;

    public PolicyPuller(URI policiesUri, Duration interval, HttpFetcher fetcher, PullRequestSigner signer) {
        if (policiesUri == null) {
            throw new IllegalArgumentException("policiesUri 不能为 null");
        }
        if (fetcher == null) {
            throw new IllegalArgumentException("fetcher 不能为 null");
        }
        if (signer == null) {
            throw new IllegalStateException(
                    "GJ-CAP-21：未装配 PullRequestSigner，拒绝发出未签名的回源请求"
                            + "（agent.pull.enabled=true 时必须先装配 HMAC 签名器）");
        }
        this.policiesUri = policiesUri;
        this.interval = interval == null ? Duration.ofSeconds(30) : interval;
        this.fetcher = fetcher;
        this.signer = signer;
    }

    /** 启动周期拉取（单线程守护线程，不阻止进程退出）。 */
    public synchronized void start() {
        if (scheduler != null) {
            return;
        }
        ScheduledExecutorService s = Executors.newSingleThreadScheduledExecutor(new ThreadFactory() {
            @Override
            public Thread newThread(Runnable r) {
                Thread t = new Thread(r, "guanjin-policy-pull");
                t.setDaemon(true);
                return t;
            }
        });
        long periodMs = Math.max(1L, interval.toMillis());
        s.scheduleWithFixedDelay(new Runnable() {
            @Override
            public void run() {
                pullOnce();
            }
        }, 0L, periodMs, TimeUnit.MILLISECONDS);
        this.scheduler = s;
    }

    /** 拉一次（失败只记账，不抛出——拉取失败不影响业务判定）。 */
    public void pullOnce() {
        attempts.incrementAndGet();
        try {
            Map<String, String> headers = signer.headers("GET", policiesUri.getRawPath());
            HttpResult res = fetcher.get(policiesUri, headers);
            lastStatus.set(res.status());
            if (res.is2xx()) {
                successes.incrementAndGet();
            }
        } catch (HttpFetchException e) {
            lastStatus.set(-2);
        } catch (RuntimeException e) {
            lastStatus.set(-3);
        }
    }

    public long attempts() {
        return attempts.get();
    }

    public long successes() {
        return successes.get();
    }

    public int lastStatus() {
        return lastStatus.get();
    }

    public URI policiesUri() {
        return policiesUri;
    }

    public Duration interval() {
        return interval;
    }

    @Override
    public synchronized void close() {
        if (scheduler != null) {
            scheduler.shutdownNow();
            scheduler = null;
        }
    }
}
