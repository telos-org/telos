package sessionapi

import (
	"errors"

	"golang.org/x/sys/unix"
)

func processExitWait(pid, cancelFD int) (func() error, func(), error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, nil, err
	}
	wait := func() error {
		fds := []unix.PollFd{
			{Fd: int32(fd), Events: unix.POLLIN},
			{Fd: int32(cancelFD), Events: unix.POLLIN},
		}
		for {
			_, err := unix.Poll(fds, -1)
			if !errors.Is(err, unix.EINTR) {
				return err
			}
		}
	}
	return wait, func() { unix.Close(fd) }, nil
}
