package guanjin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStartupSelfCheckPullsJwksAtDefaultPath 钉住 GJ-CAP-22 的三件事
// （契约 §5.1「构造」步 + AC-SDK-03）：
//
//	① `New` 内部**真的拉一次** JWKS（不是"等第一个请求再拉"）——平台桩记录到的请求数 = 1；
//	② 拉的是**默认路径** `${platform.base-url}/.well-known/jwks.json`（AC-SDK-03 的"纠错性默认值"，D6(3)）；
//	③ 拉取成功才返回可用 SDK，失败一律返回 error（下一个用例）。
func TestStartupSelfCheckPullsJwksAtDefaultPath(t *testing.T) {
	priv := genKey(t)
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody("k1", &priv.PublicKey))
	}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	cfg.Authz.ReplayStore = ReplayStoreMemory
	if _, err := New(cfg, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()),
		WithLogger(func(string, ...any) {})); err != nil {
		t.Fatalf("启动自检应通过：%v", err)
	}
	if len(gotPaths) != 1 {
		t.Fatalf("构造期应恰好拉一次 JWKS（GJ-CAP-22），实得 %d 次：%v", len(gotPaths), gotPaths)
	}
	if gotPaths[0] != "/.well-known/jwks.json" {
		t.Fatalf("默认 jwks-uri 必须是 /.well-known/jwks.json（AC-SDK-03 / D6(3)），实得 %q", gotPaths[0])
	}
}

// TestStartupSelfCheckFailsOn404And500：**404 也是失败**，不许降级成警告继续跑
// （AC-SDK-03 的判据原文「404/500 即失败」；错误码沿用 KEY_UNAVAILABLE，与运行期同码）。
func TestStartupSelfCheckFailsOn404And500(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		cfg := testConfig(t, srv.URL)
		cfg.Authz.ReplayStore = ReplayStoreMemory
		sdk, err := New(cfg, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()),
			WithLogger(func(string, ...any) {}))
		srv.Close()
		if err == nil {
			t.Fatalf("JWKS 返回 %d 时启动必须失败（AC-SDK-03），实际拿到可用 SDK", status)
		}
		if sdk != nil {
			t.Fatalf("启动失败时不得返回半可用的 SDK 实例")
		}
		var ge *Error
		if !errors.As(err, &ge) || ge.Code != CodeKeyUnavailable {
			t.Fatalf("启动自检失败应带 KEY_UNAVAILABLE（与运行期同码），实得 %v", err)
		}
		if ge.Status != http.StatusServiceUnavailable {
			t.Fatalf("KEY_UNAVAILABLE 必须是 503，实得 %d", ge.Status)
		}
	}
}

// TestStartupSelfCheckWarnsOnEmptyKeysAndDistinguishesCauses 覆盖两件事：
//
//	① **与 Java 同口径**（契约 §4.1 M2/M6）：JWKS 能拉但**没有任何可用公钥**只告警、不阻止启动
//	   （部署能起，但所有断言会 401）——写成失败会与 Java 语义分叉；
//	② AC-FC-04 后半句：「启动自检须把『密钥不可用』与『平台不可达』区分开」——两者对外同码
//	   `KEY_UNAVAILABLE`/503，但**病因必须可区分**（连接被拒 vs 404）。
func TestStartupSelfCheckWarnsOnEmptyKeysAndDistinguishesCauses(t *testing.T) {
	// ① 空 keys：启动成功 + 打出告警
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer empty.Close()
	var logs []string
	cfgEmpty := testConfig(t, empty.URL)
	cfgEmpty.Authz.ReplayStore = ReplayStoreMemory
	if _, err := New(cfgEmpty, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()),
		WithLogger(func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })); err != nil {
		t.Fatalf("空 keys 应与 Java 同口径：只告警、不阻止启动（实得 %v）", err)
	}
	warned := false
	for _, l := range logs {
		if strings.Contains(l, "没有任何 SM2 公钥") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("空 keys 必须打告警（否则「部署成功但全 401」没人知道），实际日志=%v", logs)
	}

	// ② 平台不可达（连接被拒） vs ③ 平台可达但返回 404：同码、不同病因
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	cfgDead := testConfig(t, deadURL)
	cfgDead.Authz.ReplayStore = ReplayStoreMemory
	_, errUnreachable := New(cfgDead, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()),
		WithLogger(func(string, ...any) {}))

	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv404.Close()
	cfg404 := testConfig(t, srv404.URL)
	cfg404.Authz.ReplayStore = ReplayStoreMemory
	_, err404 := New(cfg404, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()),
		WithLogger(func(string, ...any) {}))

	if errUnreachable == nil || err404 == nil {
		t.Fatalf("两种病因都必须启动失败：unreachable=%v 404=%v", errUnreachable, err404)
	}
	for _, e := range []error{errUnreachable, err404} {
		var ge *Error
		if !errors.As(e, &ge) || ge.Code != CodeKeyUnavailable || ge.Status != http.StatusServiceUnavailable {
			t.Fatalf("启动自检失败必须是 503 KEY_UNAVAILABLE（AC-FC-04），实得 %v", e)
		}
	}
	if errUnreachable.Error() == err404.Error() {
		t.Fatalf("两种病因的 message 必须可区分，实际都是：%v", err404)
	}
}
