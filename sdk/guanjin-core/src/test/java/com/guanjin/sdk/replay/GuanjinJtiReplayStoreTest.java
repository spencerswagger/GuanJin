package com.guanjin.sdk.replay;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.guanjin.sdk.GuanjinDependencyException;
import com.guanjin.sdk.GuanjinReplayException;
import java.time.Instant;
import java.util.HashSet;
import java.util.Set;
import org.junit.jupiter.api.Test;

class GuanjinJtiReplayStoreTest {

    private static final long FUTURE = Instant.now().getEpochSecond() + 60;

    @Test
    void inMemory_rejectsSecondUse() {
        GuanjinInMemoryJtiReplayStore store = new GuanjinInMemoryJtiReplayStore();
        assertThatCode(() -> store.checkAndStore("jti-1", FUTURE)).doesNotThrowAnyException();
        assertThatThrownBy(() -> store.checkAndStore("jti-1", FUTURE))
                .isInstanceOf(GuanjinReplayException.class);
        assertThatCode(() -> store.checkAndStore("jti-2", FUTURE)).doesNotThrowAnyException();
    }

    @Test
    void redis_rejectsSecondUse() {
        GuanjinRedisJtiReplayStore store = new GuanjinRedisJtiReplayStore(new FakeRedis());
        assertThatCode(() -> store.checkAndStore("jti-r1", FUTURE)).doesNotThrowAnyException();
        assertThatThrownBy(() -> store.checkAndStore("jti-r1", FUTURE))
                .isInstanceOf(GuanjinReplayException.class);
    }

    @Test
    void redis_unavailable_failsClosed() {
        GuanjinRedisCommands down = new GuanjinRedisCommands() {
            @Override
            public boolean setIfAbsent(String key, String value, int ttlSeconds) {
                throw new IllegalStateException("connection refused");
            }

            @Override
            public boolean ping() {
                return false;
            }

            @Override
            public void close() {
            }
        };
        GuanjinRedisJtiReplayStore store = new GuanjinRedisJtiReplayStore(down);
        assertThatThrownBy(() -> store.checkAndStore("jti-x", FUTURE))
                .isInstanceOf(GuanjinDependencyException.class);
    }

    @Test
    void missingJti_isRejected() {
        assertThatThrownBy(() -> new GuanjinInMemoryJtiReplayStore().checkAndStore("", FUTURE))
                .isInstanceOf(GuanjinReplayException.class);
    }

    static final class FakeRedis implements GuanjinRedisCommands {

        private final Set<String> keys = new HashSet<>();

        @Override
        public boolean setIfAbsent(String key, String value, int ttlSeconds) {
            return keys.add(key);
        }

        @Override
        public boolean ping() {
            return true;
        }

        @Override
        public void close() {
        }
    }
}
