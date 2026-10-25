package com.guanjin.sdk;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nimbusds.jose.util.JSONObjectUtils;
import java.nio.charset.StandardCharsets;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import org.bouncycastle.math.ec.ECPoint;
import org.junit.jupiter.api.Test;

class GuanjinJwksCacheTest {

    private static String jwksJson() {
        return TestVectors.read("jwks.json");
    }

    @Test
    void resolvesPublicKeyByKid() throws Exception {
        GuanjinJwksCache provider = new GuanjinJwksCache("https://p/jwks", Duration.ofMinutes(10), uri -> jwksJson());
        ECPoint key = provider.publicKey("gw:sdk-vector-0001");
        assertThat(key).isNotNull();
        assertThat(key.isValid()).isTrue();
        assertThat(provider.cachedKeyCount()).isGreaterThanOrEqualTo(1);
    }

    @Test
    void unknownKidForcesRefreshThenFails() throws Exception {
        AtomicInteger fetches = new AtomicInteger();
        GuanjinJwksCache provider = new GuanjinJwksCache("https://p/jwks", Duration.ofMinutes(10), uri -> {
            fetches.incrementAndGet();
            return jwksJson();
        });
        assertThat(provider.publicKey("gw:sdk-vector-0001")).isNotNull();
        assertThat(fetches.get()).isEqualTo(1);

        assertThatThrownBy(() -> provider.publicKey("gw:missing"))
                .isInstanceOf(GuanjinAuthenticationException.class)
                .hasMessageContaining("kid");
        assertThat(fetches.get()).isEqualTo(2);
    }

    @Test
    void expiredCache_fallsBackToOldCacheWhenFetchFails() throws Exception {
        AtomicBoolean fail = new AtomicBoolean(false);
        MutableClock clock = new MutableClock(Instant.parse("2026-01-01T00:00:00Z"));
        GuanjinJwksCache provider = new GuanjinJwksCache("https://p/jwks", Duration.ofMinutes(10), uri -> {
            if (fail.get()) {
                throw new IllegalStateException("network down");
            }
            return jwksJson();
        }, clock);

        assertThat(provider.publicKey("gw:sdk-vector-0001")).isNotNull();
        fail.set(true);
        clock.advance(Duration.ofMinutes(30));

        assertThat(provider.publicKey("gw:sdk-vector-0001")).isNotNull();
    }

    @Test
    void noCacheAndFetchFailure_throwsDependency() {
        GuanjinJwksCache provider = new GuanjinJwksCache("https://p/jwks", Duration.ofMinutes(10), uri -> {
            throw new IllegalStateException("network down");
        });
        assertThatThrownBy(() -> provider.publicKey("gw:sdk-vector-0001"))
                .isInstanceOf(GuanjinDependencyException.class);
    }

    static final class MutableClock extends Clock {

        private Instant instant;

        MutableClock(Instant instant) {
            this.instant = instant;
        }

        void advance(Duration duration) {
            instant = instant.plus(duration);
        }

        @Override
        public ZoneId getZone() {
            return ZoneOffset.UTC;
        }

        @Override
        public Clock withZone(ZoneId zone) {
            return this;
        }

        @Override
        public Instant instant() {
            return instant;
        }
    }
}
