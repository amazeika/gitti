//go:build darwin

package git

import "golang.org/x/sys/unix"

// darwinProcessIsZombie reports whether the kernel still holds pid as an
// exited-but-unreaped zombie, without reaping it and without executing any
// diagnostic tool. Only a successful kinfo read whose embedded PID matches
// the queried one is trusted, so unexpected layout or lookup results
// report unknown (false) and the caller falls through to the ps
// diagnostic.
func darwinProcessIsZombie(pid int) bool {
	if pid <= 0 {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false
	}
	if int(kp.Proc.P_pid) != pid {
		return false
	}
	const darwinSZOMB = 5 // SZOMB in bsd/sys/proc.h
	return kp.Proc.P_stat == darwinSZOMB
}
