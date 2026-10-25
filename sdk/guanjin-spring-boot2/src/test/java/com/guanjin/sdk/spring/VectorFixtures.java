package com.guanjin.sdk.spring;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;

final class VectorFixtures {

    private VectorFixtures() {
    }

    static String read(String name) {
        try {
            return new String(Files.readAllBytes(dir().resolve(name)), StandardCharsets.UTF_8);
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    static Path dir() {
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
}
