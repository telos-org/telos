package sessionapi

import (
	"errors"

	"golang.org/x/sys/unix"
)

func processExitWait(pid, cancelFD int) (func() error, func(), error) {
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, nil, err
	}
	unix.CloseOnExec(fd)
	changes := []unix.Kevent_t{
		{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT},
		{Ident: uint64(cancelFD), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ONESHOT},
	}
	if _, err := unix.Kevent(fd, changes, nil, nil); err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	wait := func() error {
		events := make([]unix.Kevent_t, 1)
		for {
			_, err := unix.Kevent(fd, nil, events, nil)
			if !errors.Is(err, unix.EINTR) {
				return err
			}
		}
	}
	return wait, func() { unix.Close(fd) }, nil
}
