package com.guanjin.spring.boot2;

import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;

import java.io.IOException;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;

/**
 * 真 Redis 连接工厂（集成用例用）。**不 mock**：地址/密码来自环境变量，连不上就红。
 *
 * <p>环境变量（与 Go 侧 CI 口径一致）：{@code GUANJIN_TEST_REDIS_ADDR}（默认
 * {@code 127.0.0.1:6379}）、{@code GUANJIN_TEST_REDIS_PASSWORD}（默认空）。
 */
final class RedisTestSupport {

    private RedisTestSupport() {
    }

    static String host() {
        return address()[0];
    }

    static int port() {
        return Integer.parseInt(address()[1]);
    }

    static String password() {
        String p = System.getenv("GUANJIN_TEST_REDIS_PASSWORD");
        return p == null ? "" : p;
    }

    static LettuceConnectionFactory factory(String password) {
        LettuceConnectionFactory f = new LettuceConnectionFactory(host(), port());
        if (password != null && !password.isEmpty()) {
            f.setPassword(password);
        }
        f.afterPropertiesSet();
        return f;
    }

    static StringRedisTemplate template() {
        return new StringRedisTemplate(factory(password()));
    }

    /**
     * 「Redis 挂了」的**环境无关**模拟：真 TCP 服务端 accept 后立刻 close（RST）。
     *
     * <p>为什么不用"错口令"：Redis 未设 {@code requirepass} 时 AUTH 报错但客户端不抛异常
     * （CI 的 redis service 就没口令），这种用例在本地绿、在 CI 红，属于环境依赖的假证据。
     * 连接被重置/端口不通才是"存储不可用"的真实形态。
     */
    static final class BlackHole implements AutoCloseable {

        private final ServerSocket server;
        private final Thread thread;

        BlackHole() throws IOException {
            server = new ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"));
            thread = new Thread(new Runnable() {
                @Override
                public void run() {
                    while (!server.isClosed()) {
                        try {
                            Socket s = server.accept();
                            s.setSoLinger(true, 0);
                            s.close();
                        } catch (IOException e) {
                            return;
                        }
                    }
                }
            }, "guanjin-blackhole-redis");
            thread.setDaemon(true);
            thread.start();
        }

        LettuceConnectionFactory factory() {
            LettuceConnectionFactory f = new LettuceConnectionFactory("127.0.0.1", server.getLocalPort());
            f.setTimeout(1000L); // 命令超时压到 1s：不可用要"快速失败"，不是挂着等
            f.afterPropertiesSet();
            return f;
        }

        @Override
        public void close() {
            try {
                server.close();
            } catch (IOException ignored) {
                // 关闭失败不影响判定
            }
        }
    }

    private static String[] address() {
        String raw = System.getenv("GUANJIN_TEST_REDIS_ADDR");
        if (raw == null || raw.trim().isEmpty()) {
            raw = "127.0.0.1:6379";
        }
        String[] parts = raw.trim().split(":");
        return new String[]{parts[0], parts.length > 1 ? parts[1] : "6379"};
    }
}
