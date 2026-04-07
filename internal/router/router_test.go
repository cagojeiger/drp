package router

import (
	"testing"
)

func TestAddAndLookup(t *testing.T) {
	r := New()

	cfg := &RouteConfig{
		Domain:    "app.example.com",
		Location:  "/",
		ProxyName: "web",
	}
	if err := r.Add(cfg); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := r.Lookup("app.example.com", "/")
	if !ok {
		t.Fatal("Lookup should find the route")
	}
	if got.ProxyName != "web" {
		t.Errorf("ProxyName = %q, want %q", got.ProxyName, "web")
	}
}

func TestLookupNotFound(t *testing.T) {
	r := New()

	_, ok := r.Lookup("unknown.com", "/")
	if ok {
		t.Error("Lookup should return false for unknown domain")
	}
}

func TestDuplicateDomain(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "web1"})
	err := r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "web2"})
	if err == nil {
		t.Error("should reject duplicate domain+location")
	}
}

func TestRemove(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "web"})
	r.Remove("web")

	_, ok := r.Lookup("app.com", "/")
	if ok {
		t.Error("Lookup should return false after Remove")
	}
}

func TestRemoveNonExistent(t *testing.T) {
	r := New()
	// 에러 없이 무시
	r.Remove("nonexistent")
}

func TestRemoveMultipleRoutes(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "a.com", Location: "/", ProxyName: "proxy1"})
	r.Add(&RouteConfig{Domain: "b.com", Location: "/", ProxyName: "proxy1"})
	r.Add(&RouteConfig{Domain: "c.com", Location: "/", ProxyName: "proxy2"})

	// proxy1 제거 → a.com, b.com 둘 다 사라져야 함
	r.Remove("proxy1")

	if _, ok := r.Lookup("a.com", "/"); ok {
		t.Error("a.com should be removed")
	}
	if _, ok := r.Lookup("b.com", "/"); ok {
		t.Error("b.com should be removed")
	}
	// c.com은 남아있어야 함
	if _, ok := r.Lookup("c.com", "/"); !ok {
		t.Error("c.com should still exist")
	}
}

func TestLongestPrefixMatch(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "root"})
	r.Add(&RouteConfig{Domain: "app.com", Location: "/api", ProxyName: "api"})
	r.Add(&RouteConfig{Domain: "app.com", Location: "/api/v2", ProxyName: "apiv2"})

	tests := []struct {
		path     string
		wantName string
	}{
		{"/", "root"},
		{"/about", "root"},
		{"/api", "api"},
		{"/api/users", "api"},
		{"/api/v2", "apiv2"},
		{"/api/v2/items", "apiv2"},
	}

	for _, tt := range tests {
		got, ok := r.Lookup("app.com", tt.path)
		if !ok {
			t.Errorf("Lookup(%q) not found", tt.path)
			continue
		}
		if got.ProxyName != tt.wantName {
			t.Errorf("Lookup(%q) = %q, want %q", tt.path, got.ProxyName, tt.wantName)
		}
	}
}

func TestWildcardDomain(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "*.example.com", Location: "/", ProxyName: "wild"})

	tests := []struct {
		domain string
		found  bool
	}{
		{"foo.example.com", true},
		{"bar.example.com", true},
		{"example.com", false},
		{"foo.bar.example.com", false},
	}

	for _, tt := range tests {
		_, ok := r.Lookup(tt.domain, "/")
		if ok != tt.found {
			t.Errorf("Lookup(%q) = %v, want %v", tt.domain, ok, tt.found)
		}
	}
}

func TestExactDomainPriority(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "*.example.com", Location: "/", ProxyName: "wild"})
	r.Add(&RouteConfig{Domain: "app.example.com", Location: "/", ProxyName: "exact"})

	// 정확한 도메인이 와일드카드보다 우선
	got, ok := r.Lookup("app.example.com", "/")
	if !ok {
		t.Fatal("Lookup should find")
	}
	if got.ProxyName != "exact" {
		t.Errorf("ProxyName = %q, want %q (exact should win over wildcard)", got.ProxyName, "exact")
	}

	// 다른 서브도메인은 와일드카드 매칭
	got, ok = r.Lookup("other.example.com", "/")
	if !ok {
		t.Fatal("Lookup should find wildcard")
	}
	if got.ProxyName != "wild" {
		t.Errorf("ProxyName = %q, want %q", got.ProxyName, "wild")
	}
}

func TestRemoveAfterAddSameDomain(t *testing.T) {
	r := New()

	r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "web"})
	r.Remove("web")

	// 제거 후 같은 도메인으로 재등록 가능
	err := r.Add(&RouteConfig{Domain: "app.com", Location: "/", ProxyName: "web2"})
	if err != nil {
		t.Errorf("re-add after remove should succeed: %v", err)
	}
}
