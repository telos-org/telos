//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package sessionapi

import "os"

func openDashboardDocFile(path string) (*os.File, error) {
	// Keep the portable fallback from following an already-visible symlink or
	// opening a blocking special file. Supported Unix runtimes additionally use
	// O_NOFOLLOW and O_NONBLOCK to close the replacement race.
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return os.Open(path)
}

func openRootRegularFile(root *os.Root, path string) (*os.File, error) {
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	opened, openedErr := file.Stat()
	after, afterErr := root.Lstat(path)
	if openedErr != nil || afterErr != nil || !opened.Mode().IsRegular() ||
		!after.Mode().IsRegular() ||
		(!os.SameFile(opened, before) && !os.SameFile(opened, after)) {
		file.Close()
		return nil, os.ErrInvalid
	}
	return file, nil
}
