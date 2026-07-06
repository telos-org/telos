package telosd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

const defaultPackageFetchTimeout = 60 * time.Second

type applyPackageMaterializer struct {
	root        string
	bundleBase  string
	bearerToken string
	client      *http.Client
}

func newApplyPackageMaterializer(root string, bearerToken string) *applyPackageMaterializer {
	return &applyPackageMaterializer{
		root:        strings.TrimSpace(root),
		bundleBase:  strings.TrimRight(strings.TrimSpace(os.Getenv("TELOS_PACKAGE_BUNDLE_BASE_URL")), "/"),
		bearerToken: strings.TrimSpace(bearerToken),
		client:      &http.Client{Timeout: packageFetchTimeout()},
	}
}

func packageFetchTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("TELOS_PACKAGE_FETCH_TIMEOUT_SEC"))
	if raw == "" {
		return defaultPackageFetchTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultPackageFetchTimeout
	}
	return time.Duration(seconds) * time.Second
}

func (m *applyPackageMaterializer) Ensure(ctx context.Context, digest string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("package materializer is not configured")
	}
	if m.root == "" {
		return "", fmt.Errorf("package root is required to materialize package %s", digest)
	}
	path, err := sessionapi.PackagePathForDigest(m.root, digest)
	if err != nil {
		return "", err
	}
	if err := sessionapi.VerifyPackageDigest(path, digest); err == nil {
		return path, nil
	}
	if m.bundleBase == "" {
		return "", fmt.Errorf("package %s is not materialized and no bundle endpoint is configured", digest)
	}
	if m.bearerToken == "" {
		return "", fmt.Errorf("runtime operator token is required to fetch package %s", digest)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create package cache dir: %w", err)
	}
	packageData, err := m.fetchPackage(ctx, digest)
	if err != nil {
		return "", err
	}
	hydrated, _, err := spec.HydrateApplyPackage(packageData, func(req spec.ApplyPackageSkillFetchRequest) ([]byte, error) {
		return m.ensureSkillBundle(ctx, req.Name, req.Digest)
	})
	if err != nil {
		return "", fmt.Errorf("hydrate package %s: %w", digest, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".package-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create package temp file: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(hydrated); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write package temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write package temp file: %w", err)
	}
	if err := sessionapi.VerifyPackageDigest(tmpPath, digest); err != nil {
		return "", fmt.Errorf("verify fetched package %s: %w", digest, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", fmt.Errorf("chmod fetched package: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("cache fetched package: %w", err)
	}
	ok = true
	return path, nil
}

func (m *applyPackageMaterializer) fetchPackage(ctx context.Context, digest string) ([]byte, error) {
	var buf bytes.Buffer
	if err := m.fetch(ctx, m.bundleURL(digest), "package "+digest, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *applyPackageMaterializer) ensureSkillBundle(ctx context.Context, name string, digest string) ([]byte, error) {
	path, err := skillBundlePathForDigest(m.root, digest)
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := spec.VerifySkillBundle(name, digest, data); err == nil {
			return data, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create skill cache dir: %w", err)
	}
	var buf bytes.Buffer
	url, err := m.skillBundleURL(digest)
	if err != nil {
		return nil, err
	}
	if err := m.fetch(ctx, url, "skill "+digest, &buf); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	if err := spec.VerifySkillBundle(name, digest, data); err != nil {
		return nil, fmt.Errorf("verify fetched skill %s: %w", digest, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skill-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create skill temp file: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write skill temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write skill temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return nil, fmt.Errorf("chmod fetched skill: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("cache fetched skill: %w", err)
	}
	ok = true
	return data, nil
}

func (m *applyPackageMaterializer) fetch(ctx context.Context, rawURL string, label string, out io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create %s fetch request: %w", label, err)
	}
	request.Header.Set("Authorization", "Bearer "+m.bearerToken)
	request.Header.Set("User-Agent", "telos-package-materializer/0")
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", label, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", label, response.StatusCode)
	}
	if _, err := io.Copy(out, response.Body); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	return nil
}

func (m *applyPackageMaterializer) bundleURL(digest string) string {
	escaped := url.PathEscape(digest)
	return strings.TrimRight(m.bundleBase, "/") + "/" + escaped + "/bundle"
}

func (m *applyPackageMaterializer) skillBundleURL(digest string) (string, error) {
	packageURL := m.bundleURL(digest)
	skillURL := strings.Replace(packageURL, "/packages/blobs/", "/skills/blobs/", 1)
	if skillURL == packageURL {
		return "", fmt.Errorf("cannot derive skill bundle endpoint from package bundle endpoint %q", packageURL)
	}
	return skillURL, nil
}

func skillBundlePathForDigest(root string, digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 {
		return "", fmt.Errorf("invalid skill digest %q", digest)
	}
	return filepath.Join(root, "skill-blobs", "sha256", hex, "skill.tar.gz"), nil
}
