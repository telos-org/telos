package local

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// terminateProcessTree sends SIGTERM to a process and all of its descendants.
// Descendants are signalled first so a parent cannot respawn them. Workers and
// the pi subprocesses they launch each form their own process group, so the
// full tree must be walked rather than relying on a single group kill.
func terminateProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	descendants := descendantPIDs(pid)
	for i := len(descendants) - 1; i >= 0; i-- {
		signalProcessGroupOrPID(descendants[i])
	}
	signalProcessGroupOrPID(pid)
}

func signalProcessGroupOrPID(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func descendantPIDs(pid int) []int {
	children := processChildren()
	var out []int
	pending := []int{pid}
	for len(pending) > 0 {
		parent := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, child := range children[parent] {
			out = append(out, child)
			pending = append(pending, child)
		}
	}
	return out
}

func processChildren() map[int][]int {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		child, err1 := strconv.Atoi(fields[0])
		parent, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[parent] = append(children[parent], child)
	}
	return children
}
