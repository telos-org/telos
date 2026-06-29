package telosd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSurfaceGatewayOpenThenProxy(t *testing.T) {
	upstreamCookies := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCookies <- r.Header.Get("Cookie")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dashboard"))
	}))
	defer upstream.Close()

	now := time.Unix(1000, 0)
	secret := []byte("env-api-key")
	route := publicRoute{
		Labels: map[string]string{publicRouteLabel: "dashboard"},
		Data: map[string]string{
			"hostname": "dashboard-auth.usetelos.ai",
			"service":  upstream.URL,
			"type":     "dashboard",
		},
	}
	gateway := newSurfaceGateway(string(secret), routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{route}, nil
		},
	})
	gateway.now = func() time.Time { return now }
	handler := gateway.Wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	openToken, err := signSurfaceToken(secret, surfaceTokenPayload{
		Version:   1,
		Kind:      "open",
		ExpiresAt: now.Add(time.Minute).Unix(),
		Host:      "dashboard-auth.usetelos.ai",
		Path:      "/admin",
		Target:    "dashboard",
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	openReq := httptest.NewRequest(http.MethodGet, "/.telos/open?t="+openToken, nil)
	openReq.Host = "dashboard-auth.usetelos.ai"
	openRes := httptest.NewRecorder()

	handler.ServeHTTP(openRes, openReq)

	if openRes.Code != http.StatusSeeOther {
		t.Fatalf("open status: got %d", openRes.Code)
	}
	if got := openRes.Header().Get("Location"); got != "/admin" {
		t.Fatalf("redirect: got %q", got)
	}
	if got := openRes.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control: got %q", got)
	}
	cookies := openRes.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != surfaceCookieName {
		t.Fatalf("cookies: %+v", cookies)
	}

	proxyReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	proxyReq.Host = "dashboard-auth.usetelos.ai"
	proxyReq.AddCookie(cookies[0])
	proxyReq.Header.Set("Cookie", cookies[0].String()+"; upstream=strip-me")
	proxyRes := httptest.NewRecorder()

	handler.ServeHTTP(proxyRes, proxyReq)

	if proxyRes.Code != http.StatusOK {
		t.Fatalf("proxy status: got %d body=%q", proxyRes.Code, proxyRes.Body.String())
	}
	if strings.TrimSpace(proxyRes.Body.String()) != "dashboard" {
		t.Fatalf("body: got %q", proxyRes.Body.String())
	}
	if got := proxyRes.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("x-frame-options: got %q", got)
	}
	if got := <-upstreamCookies; got != "" {
		t.Fatalf("upstream cookie: got %q", got)
	}

	replayRes := httptest.NewRecorder()
	handler.ServeHTTP(replayRes, openReq)
	if replayRes.Code != http.StatusUnauthorized {
		t.Fatalf("replay status: got %d", replayRes.Code)
	}

	outOfScopeReq := httptest.NewRequest(http.MethodGet, "/other", nil)
	outOfScopeReq.Host = "dashboard-auth.usetelos.ai"
	outOfScopeReq.AddCookie(cookies[0])
	outOfScopeRes := httptest.NewRecorder()
	handler.ServeHTTP(outOfScopeRes, outOfScopeReq)
	if outOfScopeRes.Code != http.StatusUnauthorized {
		t.Fatalf("out-of-scope status: got %d", outOfScopeRes.Code)
	}
}

func TestSurfaceGatewayRejectsMissingCookie(t *testing.T) {
	route := publicRoute{
		Data: map[string]string{
			"hostname": "auth.usetelos.ai",
			"service":  "http://auth.ns-auth.svc.cluster.local:8080",
		},
	}
	gateway := newSurfaceGateway("env-api-key", routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{route}, nil
		},
	})
	handler := gateway.Wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "auth.usetelos.ai"
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", res.Code)
	}
}

func TestSurfaceGatewayRejectsWrongTarget(t *testing.T) {
	now := time.Unix(1000, 0)
	secret := []byte("env-api-key")
	route := publicRoute{
		Labels: map[string]string{publicRouteLabel: "dashboard"},
		Data: map[string]string{
			"hostname": "dashboard-auth.usetelos.ai",
			"service":  "http://dashboard.ns-auth.svc.cluster.local:8080",
			"type":     "dashboard",
		},
	}
	gateway := newSurfaceGateway(string(secret), routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{route}, nil
		},
	})
	gateway.now = func() time.Time { return now }
	handler := gateway.Wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	openToken, err := signSurfaceToken(secret, surfaceTokenPayload{
		Version:   1,
		Kind:      "open",
		ExpiresAt: now.Add(time.Minute).Unix(),
		Host:      "dashboard-auth.usetelos.ai",
		Path:      "/",
		Target:    "service",
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/.telos/open?t="+openToken, nil)
	req.Host = "dashboard-auth.usetelos.ai"
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", res.Code)
	}
}

func TestSafeSurfaceWriteRejectsCrossSiteFetch(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if safeSurfaceWrite(req) {
		t.Fatal("expected cross-site write to be rejected")
	}
}

func TestSafeSurfaceWriteAllowsApprovedReferer(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Referer", "https://app.usetelos.ai/deployments/dep_test")

	if !safeSurfaceWrite(req) {
		t.Fatal("expected approved referer to be allowed")
	}
}

func TestSurfacePathAllowed(t *testing.T) {
	cases := []struct {
		requestPath string
		tokenPath   string
		want        bool
	}{
		{requestPath: "/", tokenPath: "/", want: true},
		{requestPath: "/admin", tokenPath: "/admin", want: true},
		{requestPath: "/admin/settings", tokenPath: "/admin", want: true},
		{requestPath: "/administrator", tokenPath: "/admin", want: false},
		{requestPath: "/assets/app.js", tokenPath: "/", want: true},
	}
	for _, tc := range cases {
		if got := surfacePathAllowed(tc.requestPath, tc.tokenPath); got != tc.want {
			t.Fatalf("surfacePathAllowed(%q, %q): got %v want %v", tc.requestPath, tc.tokenPath, got, tc.want)
		}
	}
}
