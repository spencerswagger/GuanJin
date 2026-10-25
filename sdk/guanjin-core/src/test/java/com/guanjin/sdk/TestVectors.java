package com.guanjin.sdk;

import com.nimbusds.jose.util.JSONObjectUtils;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.Map;

final class TestVectors {

    private TestVectors() {
    }

    static Path dir() {
        String override = System.getProperty("guanjin.testdata.dir");
        if (override != null && !override.isEmpty()) {
            return Paths.get(override);
        }
        Path cursor = Paths.get(System.getProperty("user.dir")).toAbsolutePath();
        while (cursor != null) {
            Path candidate = cursor.resolve("testdata");
            if (Files.isRegularFile(candidate.resolve("assertion.jwt"))) {
                return candidate;
            }
            candidate = cursor.resolve("sdk").resolve("testdata");
            if (Files.isRegularFile(candidate.resolve("assertion.jwt"))) {
                return candidate;
            }
            cursor = cursor.getParent();
        }
        throw new IllegalStateException("未找到 sdk/testdata 测试向量");
    }

    static String read(String name) {
        try {
            return new String(Files.readAllBytes(dir().resolve(name)), StandardCharsets.UTF_8);
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    static Map<String, Object> meta() {
        try {
            return JSONObjectUtils.parse(read("meta.json"));
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }
}
