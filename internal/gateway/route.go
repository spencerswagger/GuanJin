package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	RuntimeRoutesKey = "gateway.routes"

	MatchDomain = "domain"
	MatchPrefix = "prefix"
)

type Route struct {
	MatchType string `json:"match_type"`
	Match     string `json:"match"`
	ClientID  string `json:"client_id"`
	Upstream  string `json:"upstream"`
}

type Router interface {
	Resolve(host, path string) (Route, bool)
}

type TableRouter struct {
	domains  []Route
	prefixes []Route
}

func NewTableRouter(routes []Route) *TableRouter {
	t := &TableRouter{}
	for _, r := range routes {
		if r.Match == "" || r.ClientID == "" || r.Upstream == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(r.MatchType), MatchDomain) {
			item := r
			item.MatchType = MatchDomain
			t.domains = append(t.domains, item)
			continue
		}
		item := r
		item.MatchType = MatchPrefix
		t.prefixes = append(t.prefixes, item)
	}
	sort.SliceStable(t.prefixes, func(i, j int) bool {
		return len(t.prefixes[i].Match) > len(t.prefixes[j].Match)
	})
	return t
}

func (t *TableRouter) Resolve(host, path string) (Route, bool) {
	h := normalizeHost(host)
	for _, r := range t.domains {
		m := normalizeHost(r.Match)
		if m == "" {
			continue
		}
		if m == h {
			return r, true
		}
		if strings.HasPrefix(m, "*.") && strings.HasSuffix(h, m[1:]) {
			return r, true
		}
	}
	for _, r := range t.prefixes {
		if strings.HasPrefix(path, r.Match) {
			return r, true
		}
	}
	return Route{}, false
}

func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

func ParseRoutes(raw []byte) ([]Route, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var doc struct {
		Routes []Route `json:"routes"`
	}
	if err := json.Unmarshal(raw, &doc); err == nil && doc.Routes != nil {
		return doc.Routes, nil
	}
	var list []Route
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func RuntimeRoutes(ctx context.Context, db *gorm.DB) (*TableRouter, error) {
	if db == nil {
		return NewTableRouter(nil), nil
	}
	var row model.RuntimeConfig
	err := db.WithContext(ctx).Where("key = ?", RuntimeRoutesKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NewTableRouter(nil), nil
	}
	if err != nil {
		return nil, err
	}
	routes, err := ParseRoutes(row.Value)
	if err != nil {
		return nil, err
	}
	return NewTableRouter(routes), nil
}
