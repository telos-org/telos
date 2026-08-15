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
