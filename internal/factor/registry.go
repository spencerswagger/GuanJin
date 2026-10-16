package factor

import (
	"errors"
	"sync"
)

var ErrInvalidProvider = errors.New("factor: invalid provider")

type Registry struct {
	mu        sync.RWMutex
	providers map[FactorType]Provider
	order     []FactorType
	disabled  map[FactorType]bool
}

func NewRegistry() *Registry {
	return &Registry{
		providers: map[FactorType]Provider{},
		disabled:  map[FactorType]bool{},
	}
}

func (r *Registry) Register(p Provider) error {
	if p == nil {
		return ErrInvalidProvider
	}
	typ := p.Type()
	if typ == "" || !p.Kind().Valid() {
		return ErrInvalidProvider
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[typ]; exists {
		return ErrInvalidProvider
	}
	r.providers[typ] = p
	r.order = append(r.order, typ)
	return nil
}

func (r *Registry) Get(typ FactorType) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[typ]
	return p, ok
}

func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, typ := range r.order {
		out = append(out, r.providers[typ])
	}
	return out
}

func (r *Registry) Types() []FactorType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]FactorType{}, r.order...)
}

func (r *Registry) EnabledTypes() []FactorType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FactorType, 0, len(r.order))
	for _, typ := range r.order {
		if !r.disabled[typ] {
			out = append(out, typ)
		}
	}
	return out
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.order))
	for _, typ := range r.order {
		out = append(out, r.providers[typ].Descriptor())
	}
	return out
}

func (r *Registry) SetEnabled(typ FactorType, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[typ]; !ok {
		return
	}
	if enabled {
		delete(r.disabled, typ)
		return
	}
	r.disabled[typ] = true
}

func (r *Registry) Enabled(typ FactorType) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.providers[typ]; !ok {
		return false
	}
	return !r.disabled[typ]
}

func (r *Registry) DisabledTypes() []FactorType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FactorType, 0, len(r.disabled))
	for _, typ := range r.order {
		if r.disabled[typ] {
			out = append(out, typ)
		}
	}
	return out
}
