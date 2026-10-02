package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

func checkoutRequest(control *cloud.Client, requestID, output string) (*cloud.ChangeRequestRecord, string, error) {
	request, err := control.FindChangeRequest(requestID)
	if err != nil {
		return nil, "", err
	}
	if request.ID != requestID || request.UpdateNumber < 1 || request.PreparedPlanID == "" {
		return nil, "", fmt.Errorf("Cloud did not return an exact versioned request for %s", requestID)
	}
	reference, err := parsePackageReference(request.PackageRef)
	if err != nil {
		return nil, "", fmt.Errorf("request %s has an invalid package reference: %w", requestID, err)
	}
	data, err := control.DownloadPackageVersionBundle(reference.scope, reference.name, reference.version)
	if err != nil {
		return nil, "", err
	}
	pkg := &pulledPackage{reference: reference, digest: request.PackageDigest, data: data}
	markdown, manifest, err := verifiedPackageContents(pkg)
	if err != nil {
		return nil, "", err
	}
	localSpec := string(markdown)
	if len(manifest.Skills) > 0 {
		paths, required := map[string]string{}, map[string]bool{}
		for name, lock := range manifest.Skills {
			paths[name], required[name] = "skills/"+name, lock.Starred
		}
		localSpec, err = rewriteMergeSpec(localSpec, paths, required)
		if err != nil {
			return nil, "", err
		}
	}
	destination := strings.TrimSpace(output)
	if destination == "" {
		destination = requestID
	}
	if strings.EqualFold(filepath.Ext(destination), ".md") {
		return nil, "", fmt.Errorf("request checkout needs a complete package directory; use --output DIR")
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, "", fmt.Errorf("%s already exists", destination)
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, "", err
	}
	staging, err := os.MkdirTemp(parent, ".telos-request-checkout-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(staging)
	localPackage, err := materializePackage(control, pkg, filepath.Join(staging, "package"))
	if err != nil {
		return nil, "", err
	}
	// A checkout is editable source. Keeping the archive manifest would re-add
	// a skill that the author later removes from SPEC.md.
	if err := os.WriteFile(filepath.Join(localPackage, "SPEC.md"), []byte(localSpec), 0o644); err != nil {
		return nil, "", err
	}
	if err := os.Remove(filepath.Join(localPackage, "manifest.json")); err != nil {
		return nil, "", err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, "", fmt.Errorf("%s already exists", destination)
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	if err := os.Rename(localPackage, destination); err != nil {
		return nil, "", err
	}
	if err := recordRequestWorkspace(control, request, filepath.Join(destination, "SPEC.md")); err != nil {
		return nil, "", fmt.Errorf("request files were downloaded to %s, but request tracking could not be saved: %w", destination, err)
	}
	return request, destination, nil
}
