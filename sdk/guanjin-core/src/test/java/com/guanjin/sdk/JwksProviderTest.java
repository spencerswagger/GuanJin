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

class JwksProviderTest {

    private static String jwksJson() throws Exception {
        String json = new String(JwksProviderTest.class.getResourceAsStream("/jwt_vector.json").readAllBytes(),
                StandardCharsets.UTF_8);
        @SuppressWarnings("unchecked")
        Map<String, Object> vector = JSONObjectUtils.parse(json);
        @SuppressWarnings("unchecked")
        Map<String, Object> jwks = (Map<String, Object>) vector.get("jwks");
        return JSONObjectUtils.toJSONString(jwks);
    }

    @Test
    void resolvesPublicKeyByKid() throws Exception {
        JwksProvider provider = new JwksProvider("https://p/jwks", Duration.ofMinutes(10), uri -> jwksJson());
        ECPoint key = provider.publicKey("gw:testkey-0001");
        assertThat(key).isNotNull();
        assertThat(key.isValid()).isTrue();
        assertThat(provider.cachedKeyCount()).isGreaterThanOrEqualTo(1);
    }

    @Test
    void unknownKidForcesRefreshThenFails() throws Exception {
        AtomicInteger fetches = new AtomicInteger();
        JwksProvider provider = new JwksProvider("https://p/jwks", Duration.ofMinutes(10), uri -> {
            fetches.incrementAndGet();
            return jwksJson();
        });
        assertThat(provider.publicKey("gw:testkey-0001")).isNotNull();
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
        JwksProvider provider = new JwksProvider("https://p/jwks", Duration.ofMinutes(10), uri -> {
            if (fail.get()) {
                throw new IllegalStateException("network down");
            }
            return jwksJson();
        }, clock);

        assertThat(provider.publicKey("gw:testkey-0001")).isNotNull();
        fail.set(true);
        clock.advance(Duration.ofMinutes(30));

        assertThat(provider.publicKey("gw:testkey-0001")).isNotNull();
    }

    @Test
    void noCacheAndFetchFailure_throwsDependency() {
        JwksProvider provider = new JwksProvider("https://p/jwks", Duration.ofMinutes(10), uri -> {
            throw new IllegalStateException("network down");
        });
        assertThatThrownBy(() -> provider.publicKey("gw:testkey-0001"))
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
