package telosd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const surfaceCookieName = "__Host-telos_surface_session"

type surfaceTokenPayload struct {
	Version      int    `json:"v"`
	Kind         string `json:"kind"`
	ExpiresAt    int64  `json:"exp"`
	Host         string `json:"host"`
	Path         string `json:"path,omitempty"`
	Target       string `json:"target"`
	DeploymentID string `json:"deployment_id,omitempty"`
	Subject      string `json:"subject,omitempty"`
}

type surfaceGateway struct {
	secret   []byte
	resolver routeHandleResolver
	now      func() time.Time
	burner   *surfaceTokenBurner
}

type surfaceTokenBurner struct {
	mu   sync.Mutex
	seen map[string]int64
}

func newSurfaceGateway(secret string, resolver routeHandleResolver) surfaceGateway {
	return surfaceGateway{
		secret:   []byte(strings.TrimSpace(secret)),
		resolver: resolver,
		now:      time.Now,
		burner:   &surfaceTokenBurner{seen: map[string]int64{}},
	}
}

func (g surfaceGateway) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		route, ok := g.routeForRequest(r.Context(), r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/.telos/open" {
			g.open(w, r, route)
			return
		}
		g.proxy(w, r, route)
	}
}

func (g surfaceGateway) routeForRequest(ctx context.Context, r *http.Request) (publicRoute, bool) {
	if len(g.secret) == 0 {
		return publicRoute{}, false
	}
	host := requestHost(r)
	if host == "" {
		return publicRoute{}, false
	}
	routes, err := g.resolver.Routes(ctx)
	if err != nil {
		return publicRoute{}, false
	}
	for _, route := range routes {
		if isTCPRoute(route.Data) || !strings.EqualFold(routeHandle(route.Data), host) {
			continue
		}
		if routeService(route.Data) == "" {
			continue
		}
		return route, true
	}
	return publicRoute{}, false
}

func (g surfaceGateway) open(w http.ResponseWriter, r *http.Request, route publicRoute) {
	token := strings.TrimSpace(r.URL.Query().Get("t"))
	payload, err := verifySurfaceToken(g.secret, token, g.now())
	if err != nil || payload.Kind != "open" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !g.burner.Burn(token, payload.ExpiresAt, g.now()) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !g.tokenMatchesRoute(payload, r, route) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := cleanSurfacePath(payload.Path)
	sessionPayload := payload
	sessionPayload.Kind = "session"
	sessionPayload.ExpiresAt = g.now().Add(8 * time.Hour).Unix()
	sessionPayload.Path = path
	sessionToken, err := signSurfaceToken(g.secret, sessionPayload)
	if err != nil {
		http.Error(w, "failed to issue session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     surfaceCookieName,
		Value:    sessionToken,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Expires:  time.Unix(sessionPayload.ExpiresAt, 0).UTC(),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (g surfaceGateway) proxy(w http.ResponseWriter, r *http.Request, route publicRoute) {
	cookie, err := r.Cookie(surfaceCookieName)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	payload, err := verifySurfaceToken(g.secret, cookie.Value, g.now())
	if err != nil || payload.Kind != "session" || !g.tokenMatchesRoute(payload, r, route) || !surfacePathAllowed(r.URL.Path, payload.Path) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !safeSurfaceWrite(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	upstream, err := url.Parse(routeService(route.Data))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		http.Error(w, "route upstream is invalid", http.StatusBadGateway)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = upstream.Host
		req.Header.Del("Cookie")
		req.Header.Set("X-Forwarded-Host", requestHost(r))
		req.Header.Set("X-Forwarded-Proto", forwardedProto(r))
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		// Only the product console may frame a surface; never sibling *.usetelos.ai tenants.
		resp.Header.Set("Content-Security-Policy", "frame-ancestors 'self' https://usetelos.ai")
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func (g surfaceGateway) tokenMatchesRoute(payload surfaceTokenPayload, r *http.Request, route publicRoute) bool {
	if !strings.EqualFold(payload.Host, requestHost(r)) {
		return false
	}
	if payload.Target != surfaceTarget(route) {
		return false
	}
	return true
}

func (b *surfaceTokenBurner) Burn(token string, expiresAt int64, now time.Time) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for seenToken, seenExpiresAt := range b.seen {
		if seenExpiresAt <= now.Unix() {
			delete(b.seen, seenToken)
		}
	}
	if _, ok := b.seen[token]; ok {
		return false
	}
	b.seen[token] = expiresAt
	return true
}

func signSurfaceToken(secret []byte, payload surfaceTokenPayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifySurfaceToken(secret []byte, token string, now time.Time) (surfaceTokenPayload, error) {
	bodyText, sigText, ok := strings.Cut(token, ".")
	if !ok || bodyText == "" || sigText == "" {
		return surfaceTokenPayload{}, errors.New("invalid token")
	}
	body, err := base64.RawURLEncoding.DecodeString(bodyText)
	if err != nil {
		return surfaceTokenPayload{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigText)
	if err != nil {
		return surfaceTokenPayload{}, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return surfaceTokenPayload{}, errors.New("invalid token signature")
	}
	var payload surfaceTokenPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return surfaceTokenPayload{}, err
	}
	if payload.Version != 1 || payload.ExpiresAt <= now.Unix() || payload.Host == "" || payload.Target == "" {
		return surfaceTokenPayload{}, errors.New("expired or invalid token")
	}
	return payload, nil
}

func cleanSurfacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "/"
	}
	return path
}

func surfacePathAllowed(requestPath string, tokenPath string) bool {
	prefix := cleanSurfacePath(tokenPath)
	if prefix == "/" {
		return true
	}
	requestPath = cleanSurfacePath(requestPath)
	return requestPath == prefix || strings.HasPrefix(requestPath, strings.TrimSuffix(prefix, "/")+"/")
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	return strings.TrimSuffix(host, ".")
}

func forwardedProto(r *http.Request) string {
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func surfaceTarget(route publicRoute) string {
	if isDashboardRoute(route) {
		return "dashboard"
	}
	return "service"
}

func safeSurfaceWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	// CSRF protection: a cookie-auth write must come from the surface's own
	// origin. A sibling *.usetelos.ai tenant is same-site but not same-origin,
	// so match the request host exactly instead of trusting the whole zone.
	host := requestHost(r)
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return sameOriginHost(origin, host)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		return sameOriginHost(referer, host)
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin")
}

func sameOriginHost(rawURL, host string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Hostname(), host)
}
