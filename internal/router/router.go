package router

import (
	"fmt"
	"strings"
	"sync"
)

type RouteConfig struct {
	Domain            string
	Location          string
	ProxyName         string
	RunID             string
	UseEncryption     bool
	UseCompression    bool
	HTTPUser          string
	HTTPPwd           string
	HostHeaderRewrite string
	Headers           map[string]string
	ResponseHeaders   map[string]string
}

type Router struct {
	mu sync.RWMutex
	// domain → location → *RouteConfig (정렬: longest prefix first)
	exact    map[string][]*RouteConfig
	wildcard map[string][]*RouteConfig // "*.example.com" → key is "example.com"
}

func New() *Router {
	return &Router{
		exact:    make(map[string][]*RouteConfig),
		wildcard: make(map[string][]*RouteConfig),
	}
}

func (r *Router) Add(cfg *RouteConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	loc := cfg.Location
	if loc == "" {
		loc = "/"
	}

	domain := cfg.Domain
	store := r.exact
	key := domain
	if strings.HasPrefix(domain, "*.") {
		store = r.wildcard
		key = domain[2:] // "*.example.com" → "example.com"
	}

	// 중복 확인
	for _, existing := range store[key] {
		if existing.Location == loc {
			return fmt.Errorf("domain %s location %s already registered by %s", domain, loc, existing.ProxyName)
		}
	}

	cfg.Location = loc
	store[key] = append(store[key], cfg)
	return nil
}

func (r *Router) Remove(proxyName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	removeFrom := func(store map[string][]*RouteConfig) {
		for key, routes := range store {
			filtered := routes[:0]
			for _, rc := range routes {
				if rc.ProxyName != proxyName {
					filtered = append(filtered, rc)
				}
			}
			if len(filtered) == 0 {
				delete(store, key)
			} else {
				store[key] = filtered
			}
		}
	}

	removeFrom(r.exact)
	removeFrom(r.wildcard)
}

func (r *Router) Lookup(domain, path string) (*RouteConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. 정확한 도메인 매칭
	if cfg, ok := r.matchRoutes(r.exact[domain], path); ok {
		return cfg, true
	}

	// 2. 와일드카드 매칭: foo.example.com → "example.com"
	parts := strings.SplitN(domain, ".", 2)
	if len(parts) == 2 {
		if cfg, ok := r.matchRoutes(r.wildcard[parts[1]], path); ok {
			return cfg, true
		}
	}

	return nil, false
}

// matchRoutes finds the longest prefix match among routes.
func (r *Router) matchRoutes(routes []*RouteConfig, path string) (*RouteConfig, bool) {
	if len(routes) == 0 {
		return nil, false
	}

	var best *RouteConfig
	bestLen := -1

	for _, rc := range routes {
		if strings.HasPrefix(path, rc.Location) && len(rc.Location) > bestLen {
			best = rc
			bestLen = len(rc.Location)
		}
	}

	if best == nil {
		return nil, false
	}
	return best, true
}
