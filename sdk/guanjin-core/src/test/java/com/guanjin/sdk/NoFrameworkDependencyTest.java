package com.guanjin.sdk;

import static org.assertj.core.api.Assertions.assertThat;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.List;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;

class NoFrameworkDependencyTest {

    private static final String[] FORBIDDEN = {
            "import org.springframework.",
            "import javax.servlet.",
            "import jakarta.",
            "org.springframework.",
            "javax.servlet."
    };

    @Test
    void coreHasNoSpringOrServletImports() throws IOException {
        Path sourceRoot = locateSourceRoot();
        List<String> violations = new ArrayList<>();
        try (Stream<Path> files = Files.walk(sourceRoot)) {
            files.filter(path -> path.toString().endsWith(".java"))
                    .forEach(path -> collectViolations(path, violations));
        }
        assertThat(violations)
                .as("guanjin-core 必须零 Spring / servlet 依赖（见技术栈约定 §1.1）")
                .isEmpty();
    }

    private void collectViolations(Path path, List<String> violations) {
        try {
            List<String> lines = Files.readAllLines(path, StandardCharsets.UTF_8);
            for (int i = 0; i < lines.size(); i++) {
                String line = lines.get(i).trim();
                for (String forbidden : FORBIDDEN) {
                    if (line.contains(forbidden)) {
                        violations.add(path.getFileName() + ":" + (i + 1) + " -> " + line);
                    }
                }
            }
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    private Path locateSourceRoot() {
        Path candidate = Paths.get("src", "main", "java");
        if (Files.isDirectory(candidate)) {
            return candidate;
        }
        return Paths.get(System.getProperty("user.dir"), "src", "main", "java");
    }
}
