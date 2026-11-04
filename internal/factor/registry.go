package factor

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 是因子注册表：`FactorType → Provider` 的唯一权威。
//
// 为什么需要锁：注册只发生在启动装配期（单 goroutine），但**运行期读取**在
// 登录/补步路径上，且 CI 会开 `-race`。用 `RWMutex` 而不是"约定不许并发注册"——
// 后者是纸面约束，多副本 + 热重载一旦有人写错就是数据竞争（检测器都抓不到的那种）。
type Registry struct {
	mu     sync.RWMutex
	byType map[FactorType]Provider
}

// NewRegistry 建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{byType: make(map[FactorType]Provider)}
}

// Register 注册一个因子：nil / 类型非法 / 重复 / 描述符违规 一律报错。
//
// **先 Describe 再入库**：不合规的因子连注册表都进不去，避免"先注册、登录时才炸"。
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return ErrNilProvider
	}
	d, err := Describe(p)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byType[d.Type]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateFactor, d.Type)
	}
	// 拷贝一份描述符语义上不需要：类型与 kind 来自因子自身，运行期不会变（不可变约定）。
	r.byType[d.Type] = p
	return nil
}

// MustRegister 是 Register 的装配期便捷版：出错直接 panic。
//
// 只允许在 `cmd/*` 的启动装配里调用（那里 panic 等价于启动失败 fail-fast）；
// 业务路径禁止使用。
func (r *Registry) MustRegister(p Provider) {
	if err := r.Register(p); err != nil {
		panic(err)
	}
}

// Lookup 按类型取因子。
func (r *Registry) Lookup(t FactorType) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byType[t]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrFactorNotFound, t)
	}
	return p, nil
}

// Types 返回已注册类型（按字典序，输出稳定）。
func (r *Registry) Types() []FactorType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FactorType, 0, len(r.byType))
	for t := range r.byType {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// All 返回已注册因子（按类型字典序；管理面 `GET /api/v1/factors` 用）。
func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]FactorType, 0, len(r.byType))
	for t := range r.byType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	out := make([]Provider, 0, len(types))
	for _, t := range types {
		out = append(out, r.byType[t])
	}
	return out
}

// Descriptors 返回全部能力声明（顺序同 All），供管理面下发。
func (r *Registry) Descriptors() ([]Descriptor, error) {
	all := r.All()
	out := make([]Descriptor, 0, len(all))
	for _, p := range all {
		d, err := Describe(p)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// Authenticatable 用**能力断言**筛出可作指定角色的因子（禁止按类型挑）。
//
// 编排层（S5）选因子只走这里：某因子能不能当第二因子，由它自己实现的
// `AuthenticatableSpec` 决定，不由调用方按类型判断。
func (r *Registry) Authenticatable(role Role) []Provider {
	if !role.Valid() {
		return nil
	}
	var out []Provider
	for _, p := range r.All() {
		a, ok := p.(Authenticatable)
		if !ok {
			continue
		}
		spec := a.AuthenticatableSpec()
		if (role == RoleFirst && spec.AsFirst) || (role == RoleSecond && spec.AsSecond) {
			out = append(out, p)
		}
	}
	return out
}

// Recoverable 用能力断言筛出可参与找回的因子。
//
// 口令不会出现在这里——它没实现 `Recoverable`（基线「密码不能找回自己」）。
func (r *Registry) Recoverable() []Provider {
	var out []Provider
	for _, p := range r.All() {
		if _, ok := p.(Recoverable); ok {
			out = append(out, p)
		}
	}
	return out
}

// Registrable 用能力断言筛出可用于注册的因子。
func (r *Registry) Registrable() []Provider {
	var out []Provider
	for _, p := range r.All() {
		if _, ok := p.(Registrable); ok {
			out = append(out, p)
		}
	}
	return out
}
