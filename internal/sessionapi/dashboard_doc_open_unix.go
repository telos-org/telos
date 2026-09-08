//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package sessionapi

import (
	"errors"
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

func openRootRegularFile(root *os.Root, path string) (*os.File, error) {
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := root.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,
		0,
	)
	if errors.Is(err, syscall.ELOOP) {
		return nil, os.ErrInvalid
	}
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
