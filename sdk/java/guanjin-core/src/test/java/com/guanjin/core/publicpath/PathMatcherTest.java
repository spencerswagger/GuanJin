package com.guanjin.core.publicpath;

import org.junit.jupiter.api.Test;

import java.util.Arrays;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** GJ-CAP-19：放行即声明、deny-by-default。 */
class PathMatcherTest {

    @Test
    void builtinPathsArePublicButNoBusinessPathIs() {
        PathMatcher m = new PathMatcher(Arrays.<String>asList(), true);
        assertTrue(m.isPublic("/healthz"));
        assertTrue(m.isPublic("/readyz"));
        assertFalse(m.isPublic("/orders"));
        assertFalse(m.isPublic("/api/v1/agent/policies"));
    }

    @Test
    void builtinCanBeDisabled() {
        PathMatcher m = new PathMatcher(Arrays.<String>asList(), false);
        assertFalse(m.isPublic("/healthz"));
        assertFalse(m.isPublic("/only-if-declared"));
    }

    @Test
    void declaredPathsSupportExactAndWildcard() {
        PathMatcher m = new PathMatcher(Arrays.asList("/assets/*", "/openapi.json", "/tenant/*/public"), true);
        assertTrue(m.isPublic("/assets/app.css"));
        assertTrue(m.isPublic("/assets/img/logo.png"));
        assertTrue(m.isPublic("/openapi.json"));
        assertFalse(m.isPublic("/openapi.json/secret"));
        assertFalse(m.isPublic("/orders"));
        assertTrue(m.isPublic("/assets"));
    }

    @Test
    void trailingSlashIsNormalized() {
        PathMatcher m = new PathMatcher(Arrays.<String>asList(), true);
        assertTrue(m.isPublic("/healthz/"));
        assertFalse(m.isPublic(""), "空路径归一为 /，不属于任何放行项");
    }
}
