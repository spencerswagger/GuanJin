package com.guanjin.sdk.replay;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;

public final class SocketRedisCommands implements RedisCommands {

    private static final byte[] CRLF = {'\r', '\n'};

    private final String host;
    private final int port;
    private final String password;
    private final int timeoutMillis;

    public SocketRedisCommands(String host, int port, String password, int timeoutMillis) {
        this.host = host;
        this.port = port;
        this.password = password;
        this.timeoutMillis = timeoutMillis > 0 ? timeoutMillis : 2000;
    }

    @Override
    public boolean setIfAbsent(String key, String value, int ttlSeconds) {
        Object reply = execute("SET", key, value, "NX", "EX", Integer.toString(Math.max(ttlSeconds, 1)));
        return reply != null;
    }

    @Override
    public boolean ping() {
        try (Socket socket = open()) {
            authenticate(socket);
            Object reply = exchange(socket, "PING");
            return "PONG".equals(reply);
        } catch (IOException e) {
            throw new IllegalStateException("redis 连接失败: " + e.getMessage(), e);
        }
    }

    @Override
    public void close() {
    }

    private Object execute(String... args) {
        try (Socket socket = open()) {
            authenticate(socket);
            return exchange(socket, args);
        } catch (IOException e) {
            throw new IllegalStateException("redis 连接失败: " + e.getMessage(), e);
        }
    }

    private Socket open() throws IOException {
        Socket socket = new Socket();
        socket.connect(new InetSocketAddress(host, port), timeoutMillis);
        socket.setSoTimeout(timeoutMillis);
        return socket;
    }

    private void authenticate(Socket socket) throws IOException {
        if (password == null || password.isEmpty()) {
            return;
        }
        Object reply = exchange(socket, "AUTH", password);
        if (!"OK".equals(reply)) {
            throw new IOException("redis AUTH 失败");
        }
    }

    private Object exchange(Socket socket, String... args) throws IOException {
        OutputStream out = socket.getOutputStream();
        out.write(('*' + Integer.toString(args.length) + "\r\n").getBytes(StandardCharsets.US_ASCII));
        for (String arg : args) {
            byte[] raw = arg.getBytes(StandardCharsets.UTF_8);
            out.write(('$' + Integer.toString(raw.length) + "\r\n").getBytes(StandardCharsets.US_ASCII));
            out.write(raw);
            out.write(CRLF);
        }
        out.flush();
        return readReply(socket.getInputStream());
    }

    private Object readReply(InputStream in) throws IOException {
        int type = in.read();
        if (type < 0) {
            throw new IOException("redis 连接被关闭");
        }
        switch (type) {
            case '+':
                return readLine(in);
            case '-':
                throw new IOException("redis 错误: " + readLine(in));
            case ':':
                return Long.parseLong(readLine(in));
            case '$':
                return readBulk(in);
            default:
                throw new IOException("redis 未知回复类型: " + (char) type);
        }
    }

    private byte[] readBulk(InputStream in) throws IOException {
        int length = Integer.parseInt(readLine(in));
        if (length < 0) {
            return null;
        }
        byte[] data = new byte[length];
        int read = 0;
        while (read < length) {
            int n = in.read(data, read, length - read);
            if (n < 0) {
                throw new IOException("redis 批量回复被截断");
            }
            read += n;
        }
        in.read();
        in.read();
        return data;
    }

    private String readLine(InputStream in) throws IOException {
        StringBuilder sb = new StringBuilder();
        int b;
        while ((b = in.read()) != -1) {
            if (b == '\r') {
                in.read();
                break;
            }
            sb.append((char) b);
        }
        return sb.toString();
    }
}
