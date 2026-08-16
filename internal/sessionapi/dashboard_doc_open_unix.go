//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package sessionapi

import (
	"os"
	"syscall"
)

// O_NOFOLLOW rejects final-component symlinks; O_NONBLOCK prevents a FIFO
// replacement from blocking telosd.
func openDashboardDocFile(path string) (*os.File, error) {
	return os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,
		0,
	)
}
