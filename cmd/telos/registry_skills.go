package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func prepareRegistrySkills(specPath string) error {
	refs, err := spec.RegistrySkillRefs(specPath)
	if err != nil {
		return err
	}
	var client *cloud.Client
	for _, ref := range refs {
		if ref.Version == "" || registrySkillCached(ref) {
			continue
		}
		if client == nil {
			client, err = cloud.ControlClient()
			if err != nil {
				return fmt.Errorf("resolve registry skills: %w", err)
			}
		}
		if err := cacheRegistrySkill(client, ref); err != nil {
			return err
		}
	}
	return nil
}

func registrySkillCached(ref spec.RegistrySkillRef) bool {
	path := spec.RegistrySkillPath(ref)
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

func cacheRegistrySkill(client *cloud.Client, ref spec.RegistrySkillRef) error {
	record, err := client.GetSkillVersion(ref.Scope, ref.Name, ref.Version)
	if err != nil {
		return fmt.Errorf("resolve skill %s: %w", ref.Ref, err)
	}
	if record.Scope != ref.Scope || record.Name != ref.Name || record.Version != ref.Version {
		return fmt.Errorf("resolve skill %s: registry returned %s", ref.Ref, record.Ref)
	}
	if strings.TrimSpace(record.Digest) == "" {
		return fmt.Errorf("resolve skill %s: registry returned an empty digest", ref.Ref)
	}
	bundle, err := client.DownloadSkillVersionBundle(ref.Scope, ref.Name, ref.Version)
	if err != nil {
		return fmt.Errorf("download skill %s: %w", ref.Ref, err)
	}
	dest := spec.RegistrySkillPath(ref)
	if dest == "" {
		return fmt.Errorf("resolve skill %s: registry skill cache is unavailable", ref.Ref)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create registry skill cache: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, "."+ref.Version+"-")
	if err != nil {
		return fmt.Errorf("create registry skill cache entry: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := spec.ExtractSkillBundle(ref.Name, record.Digest, bundle, tmp); err != nil {
		return fmt.Errorf("verify skill %s: %w", ref.Ref, err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("replace registry skill cache entry: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install registry skill %s: %w", ref.Ref, err)
	}
	return nil
}
