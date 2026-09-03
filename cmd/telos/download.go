package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

var workspaceDownloadSessionIDRE = regexp.MustCompile(`^sess_[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func cmdDownload(args []string) {
	fs := newCommandFlagSet("download", "telos download SESSION [flags]")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one Cloud SESSION")

	sessionID := strings.TrimSpace(fs.Arg(0))
	if isLocalApplyID(sessionID) {
		exitWithError(errors.New("workspace download is only available for Cloud sessions"))
	}
	destination, err := workspaceArchiveDestination(sessionID)
	if err != nil {
		exitWithError(err)
	}
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		exitWithError(err)
	}
	destination, err = downloadWorkspaceArchive(control, sessionID, destination)
	if err != nil {
		exitWithError(err)
	}

	fmt.Fprintln(os.Stdout, "downloaded workspace")
	fmt.Fprintln(os.Stdout)
	printSummaryField(os.Stdout, "Session", sessionID)
	printSummaryField(os.Stdout, "Context", control.ContextName())
	printSummaryField(os.Stdout, "Path", destination)
}

func workspaceArchiveDestination(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !workspaceDownloadSessionIDRE.MatchString(sessionID) {
		return "", fmt.Errorf("invalid Cloud session ID %q", sessionID)
	}
	return "telos-workspace-" + sessionID + ".tar.gz", nil
}

func downloadWorkspaceArchive(
	control *cloud.Client,
	sessionID string,
	destination string,
) (string, error) {
	if control == nil {
		return "", errors.New("cloud client is required")
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("workspace archive destination is required")
	}
	destination = filepath.Clean(destination)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("%s already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(
		filepath.Dir(destination),
		"."+filepath.Base(destination)+".*.partial",
	)
	if err != nil {
		return "", err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := control.DownloadSessionWorkspaceArchive(sessionID, file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("download workspace: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s already exists", destination)
		}
		return "", err
	}
	return destination, nil
}
