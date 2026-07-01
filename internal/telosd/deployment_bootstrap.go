package telosd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func startDeploymentBootstrapper(ctx context.Context, store sessionapi.Store) {
	if os.Getenv("TELOS_DEPLOYMENT_BOOTSTRAP_ENABLED") == "0" {
		return
	}
	name := strings.TrimSpace(os.Getenv("TELOS_DEPLOYMENT_NAME"))
	digest := strings.TrimSpace(os.Getenv("TELOS_PACKAGE_DIGEST"))
	if name == "" || digest == "" {
		return
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			done, err := reconcileDeploymentBootstrap(store, name, digest)
			if err != nil {
				log.Printf("deployment bootstrap failed: %v", err)
			}
			if done {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func reconcileDeploymentBootstrap(store sessionapi.Store, name string, digest string) (bool, error) {
	sessions, err := store.List()
	if err != nil {
		return false, fmt.Errorf("list sessions: %w", err)
	}
	if existing, ok := activeRootSessionsByName(sessions)[name]; ok {
		if sessionPackageDigest(existing) == digest {
			return true, nil
		}
	}
	_, err = store.UpdateSpec(name, sessionapi.SessionSpecUpdateRequest{
		PackageDigest: digest,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
