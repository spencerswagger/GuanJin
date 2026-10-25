package com.guanjin.sdk.replay;

public interface GuanjinRedisCommands extends AutoCloseable {

    boolean setIfAbsent(String key, String value, int ttlSeconds);

    boolean ping();

    @Override
    void close();
}
