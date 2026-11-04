package factor

import (
	"errors"
	"sync"
	"testing"
)

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(newPassword()); err != nil {
		t.Fatalf("注册 password 失败: %v", err)
	}
	if err := r.Register(newSMS()); err != nil {
		t.Fatalf("注册 sms 失败: %v", err)
	}

	p, err := r.Lookup(FactorPassword)
	if err != nil || p.Type() != FactorPassword {
		t.Fatalf("Lookup(password) = %v, %v", p, err)
	}
	if _, err := r.Lookup(FactorTOTP); !errors.Is(err, ErrFactorNotFound) {
		t.Fatalf("未注册类型应报 ErrFactorNotFound，实际 %v", err)
	}

	// 类型列表按字典序，输出稳定（管理面展示与测试都依赖这一点）。
	types := r.Types()
	if len(types) != 2 || types[0] != FactorPassword || types[1] != FactorSMSOTP {
		t.Fatalf("Types() = %v，期望 [password sms_otp]", types)
	}
	all := r.All()
	if len(all) != 2 || all[0].Type() != FactorPassword {
		t.Fatalf("All() 顺序不对: %v", all)
	}
}

func TestRegistryRejectsBadRegistration(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrNilProvider) {
		t.Fatalf("注册 nil 应报 ErrNilProvider，实际 %v", err)
	}
	// 描述符不合规的因子连注册表都进不去。
	if err := r.Register(struct{ prov }{prov{t: "no_caps", k: KindHolding}}); !errors.Is(err, ErrDescriptorInvalid) {
		t.Fatalf("不合规因子应报 ErrDescriptorInvalid，实际 %v", err)
	}
	if err := r.Register(newPassword()); err != nil {
		t.Fatalf("首次注册应成功: %v", err)
	}
	// 同类型重复注册必须报错（否则运行期谁生效取决于注册顺序 = 隐性行为）。
	if err := r.Register(newPassword()); !errors.Is(err, ErrDuplicateFactor) {
		t.Fatalf("重复注册应报 ErrDuplicateFactor，实际 %v", err)
	}
}

// MustRegister 只给装配期用：出错必须 panic（fail-fast），不许静默忽略。
func TestRegistryMustRegisterPanics(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newPassword())

	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister 重复注册必须 panic")
		}
	}()
	r.MustRegister(newPassword())
}

func TestRegistryDescriptors(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newPassword())
	r.MustRegister(newSMS())
	ds, err := r.Descriptors()
	if err != nil {
		t.Fatalf("Descriptors: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("应有 2 份描述符，实际 %d", len(ds))
	}
	if ds[0].Type != FactorPassword || ds[1].Type != FactorSMSOTP {
		t.Fatalf("描述符顺序应与 Types() 一致: %v", ds)
	}
	if len(ds[0].Capabilities) == 0 {
		t.Fatal("描述符必须带能力列表（管理面 GET /api/v1/factors 依赖它）")
	}
}

// 选因子只能靠能力断言：口令**永远**不会出现在"可作找回凭证"里。
func TestRegistryCapabilitySelection(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newPassword())
	r.MustRegister(newSMS())
	r.MustRegister(newRecoveryCode())

	first := r.Authenticatable(RoleFirst)
	if len(first) != 2 { // password + sms
		t.Fatalf("可作第一因子的应是 password/sms，实际 %d", len(first))
	}
	for _, p := range first {
		if p.Type() == FactorRecoveryCode {
			t.Fatal("恢复码不能当认证因子（基线 §4.3）")
		}
	}

	second := r.Authenticatable(RoleSecond)
	if len(second) != 1 || second[0].Type() != FactorSMSOTP {
		t.Fatalf("可作第二因子的应只有 sms（TOTP 是 M2），实际 %v", second)
	}

	recoverable := r.Recoverable()
	if len(recoverable) != 2 { // sms + recovery_code
		t.Fatalf("可参与找回的应是 sms/recovery_code，实际 %d", len(recoverable))
	}
	for _, p := range recoverable {
		if p.Type() == FactorPassword {
			t.Fatal("密码绝不能参与找回（基线 §4.3「最容易设计错的地方」）")
		}
	}

	registrable := r.Registrable()
	if len(registrable) != 2 { // password + sms
		t.Fatalf("可用于注册的应是 password/sms，实际 %d", len(registrable))
	}
	for _, p := range registrable {
		if p.Type() == FactorRecoveryCode {
			t.Fatal("恢复码不能用于注册（需先由 TOTP 签发）")
		}
	}

	if got := r.Authenticatable(Role("third")); got != nil {
		t.Fatalf("非法 role 应返回 nil，实际 %v", got)
	}
}

// 并发读 + 装配期写的竞态（CI 会带 -race 跑同一条）。
func TestRegistryConcurrentReads(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newPassword())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, err := r.Lookup(FactorPassword); err != nil {
					t.Errorf("Lookup: %v", err)
					return
				}
				_ = r.Authenticatable(RoleFirst)
				_ = r.Types()
				_ = r.All()
			}
		}()
	}
	// 同时做一次写，验证读写不互踩。
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.Register(newSMS()); err != nil {
			t.Errorf("Register: %v", err)
		}
	}()
	wg.Wait()
}
