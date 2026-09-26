//go:build !unix

package git

import "os"

// pushDirectProcessAlreadyExited cannot observe kernel state without Unix
// syscalls, so liveness stays unknown (false) and a genuinely live kill is
// still reported as cancelled.
func pushDirectProcessAlreadyExited(int) bool {
	return false
}

// pushDirectProcessSettledDead cannot observe kernel state without Unix
// syscalls, so liveness stays unknown (false) and a genuinely live kill is
// still reported as cancelled.
func pushDirectProcessSettledDead(int) bool {
	return false
}

// pushCancelSignalDirectProcess keeps the existing non-Unix process
// semantics: without Unix signal delivery the handshake terminates the
// direct child with Kill, and the undeterminable reaped status below
// preserves the genuine live-kill cancellation claim.
func pushCancelSignalDirectProcess(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	return proc.Kill() == nil
}

// pushProcessKilledBySigTerm cannot identify the terminating signal without
// Unix wait status, so an undeterminable status preserves the cancellation
// claim for a genuine live kill.
func pushProcessKilledBySigTerm(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	return true
}

// pushProcessKilledBySigKill cannot identify the terminating signal without
// Unix wait status, so an undeterminable status preserves the cancellation
// claim for a genuine live kill.
func pushProcessKilledBySigKill(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	return true
}
