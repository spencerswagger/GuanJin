package holding

import (
	"github.com/spencerswagger/guanjin/internal/factor"
)

// SelfCheck 是**装配期自检**的唯一入口：把因子交给 SPI 的 `Describe` 跑一遍能力模型校验。
//
// 为什么装配期就要跑：能力声明的违规（如 channel 型却实现了 `Resettable`、
// 声明了 `AAL3`、`authenticatable` 却没写 `acr.sole`）在运行期表现为"某个登录方式莫名不可用"，
// 排查成本极高；在装配期它是一次启动失败。
func SelfCheck(p factor.Provider) (factor.Descriptor, error) {
	return factor.Describe(p)
}

// Register 把若干因子注册进注册表，并返回它们的能力声明（供管理面/日志展示）。
//
// `factor.Registry.Register` 内部同样会 `Describe`；这里再自检一遍是为了**在注册前**
// 就把"哪个因子不合规"报出来（错误信息里带类型），而不是让调用方从注册表错误里反推。
func Register(reg *factor.Registry, providers ...factor.Provider) ([]factor.Descriptor, error) {
	if reg == nil {
		return nil, ErrNilRegistry
	}
	out := make([]factor.Descriptor, 0, len(providers))
	for _, p := range providers {
		if p == nil {
			return nil, ErrNilProvider
		}
		d, err := SelfCheck(p)
		if err != nil {
			return nil, err
		}
		if err := reg.Register(p); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
