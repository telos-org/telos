package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/telos-org/telos/internal/bundlelimits"
)

const installedSkillPathFile = ".telos-skill-path"

type installationUpdate struct {
	Version    string
	Components []string
}

type releaseReplacement struct {
	component string
	target    string
	stage     string
	directory bool
}

func updateInstallation(target, artifact string, manifest cliReleaseManifest, baseURL, sums string, client *http.Client) (installationUpdate, error) {
	result := installationUpdate{Version: manifest.Version}
	installDir := filepath.Dir(target)
	lock, err := os.OpenFile(filepath.Join(installDir, ".telos-install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return result, fmt.Errorf("cannot lock installation (check directory permissions): %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, fmt.Errorf("another installation update is in progress: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	skillTarget, err := installedSkillTarget(installDir)
	if err != nil {
		return result, err
	}
	skillArtifact := ""
	for _, skill := range manifest.Skills {
		if skill.Ref == "@telos/telos-cli:"+strings.TrimPrefix(manifest.Version, "v") {
			if skill.Artifact != "telos-cli-skill.tar.gz" || skillArtifact != "" {
				return result, fmt.Errorf("invalid CLI skill artifact in release manifest")
			}
			skillArtifact = skill.Artifact
		}
	}
	if skillArtifact == "" {
		return result, fmt.Errorf("release %s has no matching telos-cli skill", manifest.Version)
	}

	var replacements []*releaseReplacement
	defer func() {
		for _, replacement := range replacements {
			os.RemoveAll(replacement.stage)
		}
	}()
	stageBinary := func(name, path, artifact string) error {
		replacement, err := stageReleaseBinary(name, path, artifact, baseURL, sums, client)
		if replacement != nil {
			replacements = append(replacements, replacement)
		}
		return err
	}
	runtimePath := filepath.Join(installDir, "telosd")
	if _, err := os.Lstat(runtimePath); err == nil {
		runtimeTarget, err := cliUpdateTarget(runtimePath)
		if err != nil {
			return result, fmt.Errorf("resolve installed telosd: %w", err)
		}
		runtimeArtifact := "telosd-" + strings.TrimPrefix(artifact, "telos-")
		found := false
		for _, platform := range manifest.Platforms {
			if platform.Telos == artifact && platform.Telosd == runtimeArtifact {
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("release %s has no matching telosd artifact", manifest.Version)
		}
		if err := stageBinary("telosd", runtimeTarget, runtimeArtifact); err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}

	replacement, err := stageReleaseSkill(skillTarget, skillArtifact, baseURL, sums, client)
	if replacement != nil {
		replacements = append(replacements, replacement)
	}
	if err != nil {
		return result, err
	}
	pathRecord, err := stageReleaseFile("", filepath.Join(installDir, installedSkillPathFile), []byte(skillTarget+"\n"), 0o644)
	if pathRecord != nil {
		replacements = append(replacements, pathRecord)
	}
	if err != nil {
		return result, err
	}
	// Replace the CLI last, once every companion artifact has been verified.
	if err := stageBinary("telos", target, artifact); err != nil {
		return result, err
	}
	if err := installReleaseReplacements(replacements); err != nil {
		return result, err
	}
	for _, replacement := range replacements {
		if replacement.component != "" {
			result.Components = append(result.Components, replacement.component)
		}
	}
	return result, nil
}

func installedSkillTarget(installDir string) (string, error) {
	root := os.Getenv("TELOS_AGENT_SKILLS_DIR")
	target := ""
	if root != "" {
		target = filepath.Join(root, "telos-cli")
	} else if data, err := os.ReadFile(filepath.Join(installDir, installedSkillPathFile)); err == nil {
		target = strings.TrimSuffix(string(data), "\n")
		if !filepath.IsAbs(target) || filepath.Base(target) != "telos-cli" {
			return "", fmt.Errorf("invalid installed skill path; set TELOS_AGENT_SKILLS_DIR to its parent directory")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		target = filepath.Join(home, ".agents", "skills", "telos-cli")
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	return target, nil
}

func stageReleaseBinary(component, target, artifact, baseURL, sums string, client *http.Client) (*releaseReplacement, error) {
	expected, err := cliReleaseChecksum(sums, artifact)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o755 {
		file, err := os.Open(target)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil {
			return nil, err
		}
		if bytes.Equal(hash.Sum(nil), expected) {
			return nil, nil
		}
	}
	stage, err := os.CreateTemp(filepath.Dir(target), ".telos-update-*")
	if err != nil {
		return nil, fmt.Errorf("cannot stage %s update (check directory permissions): %w", component, err)
	}
	replacement := &releaseReplacement{component: component, target: target, stage: stage.Name()}
	defer stage.Close()
	hash := sha256.New()
	if err := downloadCLIUpdate(client, baseURL+"/"+artifact, io.MultiWriter(stage, hash), 256<<20); err != nil {
		return replacement, err
	}
	if !bytes.Equal(hash.Sum(nil), expected) {
		return replacement, fmt.Errorf("checksum verification failed for %s; installed components are unchanged", artifact)
	}
	if err := stage.Chmod(0o755); err != nil {
		return replacement, err
	}
	if err := stage.Sync(); err != nil {
		return replacement, err
	}
	return replacement, stage.Close()
}

func stageReleaseFile(component, target string, data []byte, mode fs.FileMode) (*releaseReplacement, error) {
	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
		return nil, nil
	}
	stage, err := os.CreateTemp(filepath.Dir(target), ".telos-update-*")
	if err != nil {
		return nil, err
	}
	replacement := &releaseReplacement{component: component, target: target, stage: stage.Name()}
	defer stage.Close()
	if _, err := stage.Write(data); err != nil {
		return replacement, err
	}
	if err := stage.Chmod(mode); err != nil {
		return replacement, err
	}
	if err := stage.Sync(); err != nil {
		return replacement, err
	}
	return replacement, stage.Close()
}

func stageReleaseSkill(target, artifact, baseURL, sums string, client *http.Client) (*releaseReplacement, error) {
	if _, err := os.Lstat(target); err == nil {
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			return nil, err
		}
		target = resolved
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	expected, err := cliReleaseChecksum(sums, artifact)
	if err != nil {
		return nil, err
	}
	var data bytes.Buffer
	if err := downloadCLIUpdate(client, baseURL+"/"+artifact, &data, bundlelimits.MaxCompressedBytes); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data.Bytes())
	if !bytes.Equal(hash[:], expected) {
		return nil, fmt.Errorf("checksum verification failed for %s; installed components are unchanged", artifact)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".telos-update-*")
	if err != nil {
		return nil, fmt.Errorf("cannot stage skill update (check directory permissions): %w", err)
	}
	replacement := &releaseReplacement{component: "telos-cli skill", target: target, stage: stage, directory: true}
	if err := os.Chmod(stage, 0o755); err != nil {
		return replacement, err
	}
	if err := extractReleaseSkill(data.Bytes(), stage); err != nil {
		return replacement, err
	}
	current, err := releaseFileTree(target)
	if os.IsNotExist(err) {
		return replacement, nil
	}
	if err != nil {
		return replacement, err
	}
	next, err := releaseFileTree(stage)
	if err != nil {
		return replacement, err
	}
	if maps.Equal(current, next) {
		os.RemoveAll(stage)
		return nil, nil
	}
	return replacement, nil
}

func extractReleaseSkill(data []byte, target string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open CLI skill: %w", err)
	}
	defer gz.Close()
	archive := &io.LimitedReader{R: gz, N: bundlelimits.MaxArchiveBytes + 1}
	reader := tar.NewReader(archive)
	seen := map[string]bool{}
	var expanded int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read CLI skill: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		if !filepath.IsLocal(name) || len(header.Name) > bundlelimits.MaxPathBytes || seen[name] {
			return fmt.Errorf("invalid or duplicate CLI skill entry %q", header.Name)
		}
		seen[name] = true
		if len(seen) > bundlelimits.MaxFiles {
			return fmt.Errorf("CLI skill contains too many entries")
		}
		path := filepath.Join(target, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > bundlelimits.MaxEntryBytes || header.Size > bundlelimits.MaxExpandedBytes-expanded {
				return fmt.Errorf("CLI skill expanded content exceeds its size limit")
			}
			expanded += header.Size
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported CLI skill entry %q", header.Name)
		}
	}
	if _, err := io.Copy(io.Discard, archive); err != nil {
		return err
	}
	if archive.N == 0 {
		return fmt.Errorf("CLI skill archive exceeds its size limit")
	}
	info, err := os.Stat(filepath.Join(target, "SKILL.md"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("CLI skill is missing SKILL.md")
	}
	return nil
}

type releaseFile struct {
	mode fs.FileMode
	hash [sha256.Size]byte
}

func releaseFileTree(root string) (map[string]releaseFile, error) {
	files := map[string]releaseFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file := releaseFile{mode: info.Mode()}
		if info.Mode().IsRegular() {
			reader, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, readErr := io.Copy(hash, reader)
			closeErr := reader.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return err
			}
			copy(file.hash[:], hash.Sum(nil))
		}
		files[name] = file
		return nil
	})
	return files, err
}

// installReleaseReplacements renames each verified artifact over its target.
// Every artifact is staged beside its target, so a file is replaced by one
// atomic rename and is never missing or half-written. The CLI comes last, so a
// failed rename leaves a working CLI to run the update again with.
func installReleaseReplacements(replacements []*releaseReplacement) error {
	var updated []string
	for _, replacement := range replacements {
		if err := replaceReleaseTarget(replacement); err != nil {
			if len(updated) > 0 {
				err = fmt.Errorf("%w; updated %s; run `telos update` again to finish", err, strings.Join(updated, ", "))
			}
			return err
		}
		if replacement.component != "" {
			updated = append(updated, replacement.component)
		}
	}
	return nil
}

func replaceReleaseTarget(replacement *releaseReplacement) error {
	if !replacement.directory {
		if err := os.Rename(replacement.stage, replacement.target); err != nil {
			return fmt.Errorf("replace %s: %w", replacement.target, err)
		}
		return nil
	}
	// A directory cannot be renamed over another, so the previous one moves
	// aside first and comes back if the new one cannot take its place.
	previous := replacement.stage + ".previous"
	if err := os.Rename(replacement.target, previous); os.IsNotExist(err) {
		previous = ""
	} else if err != nil {
		return fmt.Errorf("replace %s: %w", replacement.target, err)
	}
	if err := os.Rename(replacement.stage, replacement.target); err != nil {
		err = fmt.Errorf("replace %s: %w", replacement.target, err)
		if previous != "" {
			if restoreErr := os.Rename(previous, replacement.target); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s from %s: %w", replacement.target, previous, restoreErr))
			}
		}
		return err
	}
	if previous != "" {
		os.RemoveAll(previous)
	}
	return nil
}
