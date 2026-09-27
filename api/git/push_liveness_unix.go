//go:build unix

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// pushDirectProcessKernelState returns (exited, definitive) without
// executing any diagnostic tool.
// exited reports observed death. definitive reports whether the verdict
// decides the Kill.
// Definitive death suppresses Kill. Definitive life keeps the candidate.
// Death is ESRCH, Linux /proc Z/X, or Darwin zombie.
// Life is a live /proc observation.
// EPERM alone is unknown. Unknown stays a live candidate for ps.
// An unknown verdict permits a signal attempt. That attempt still needs
// signal success plus a matching reaped SIGTERM status to keep a
// cancellation claim.
func pushDirectProcessKernelState(pid int) (exited, definitive bool) {
	if pid <= 0 {
		return false, false
	}
	// A vanished PID needs no tooling to identify: signal 0 delivers
	// nothing but reports ESRCH once the PID no longer exists. This covers
	// platforms without /proc and environments where ps cannot execute, so
	// a Kill that lands after independent termination cannot claim
	// causation. A zombie still holds its PID, so ESRCH never fires for an
	// unreaped child; live, zombie and permission-denied (EPERM) PIDs fall
	// through to the state check below.
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true, true
		}
	} else {
		// Signal 0 succeeded: the PID exists, but a zombie also exists
		// until reaped, so existence alone proves nothing. Fall through
		// to the zombie-state check below.
	}
	if runtime.GOOS == "linux" {
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if idx := bytes.LastIndexByte(stat, ')'); idx >= 0 {
				j := idx + 1
				for j < len(stat) && (stat[j] == ' ' || stat[j] == '\t') {
					j++
				}
				if j < len(stat) {
					switch stat[j] {
					case 'Z', 'X', 'x':
						return true, true
					default:
						return false, true
					}
				}
			}
			return false, true
		} else if os.IsNotExist(err) {
			// A missing /proc entry after a non-ESRCH signal-0 probe is
			// not verified death: procfs may be unavailable (chroot,
			// unmounted /proc) while the child is still live. Confirm
			// ESRCH independently; otherwise unknown liveness so the
			// live child stays a signal candidate.
			if kerr := syscall.Kill(pid, 0); kerr != nil && errors.Is(kerr, syscall.ESRCH) {
				return true, true
			}
			return false, false
		}
	}
	// Without /proc, an exited-but-unreaped child is still positively
	// identifiable through the kernel's zombie state; anything else (a
	// live child or an undeterminable state) is non-definitive and falls
	// through to the ps diagnostic.
	if darwinProcessIsZombie(pid) {
		return true, true
	}
	return false, false
}

// pushDirectProcessSettledDead polls the fast kernel-state checks for a
// bounded interval.
// The ps diagnostic exec spans milliseconds. An independent death inside
// that window can be missed by a single immediate recheck while its zombie
// is still materializing.
// Polling lets such a materializing zombie become visible before Kill.
// Polling does not close the final observation-to-signal gap. A death
// after the last poll sample still races the Kill. Polling is not atomic
// proof that the signal caused death.
// A still-live child exhausts the budget and stays a cancellation
// candidate. Expiry falls back to Kill, so genuine cancellation is never
// suppressed.
// Only verified dead/zombie evidence suppresses the kill. An
// undeterminable state stays a live candidate. The reaped SIGTERM status
// check remains the second consistency filter.
func pushDirectProcessSettledDead(pid int) bool {
	const settleBudget = 100 * time.Millisecond
	const settleInterval = 5 * time.Millisecond
	deadline := time.Now().Add(settleBudget)
	for {
		if exited, _ := pushDirectProcessKernelState(pid); exited {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(settleInterval)
	}
}

// pushDirectProcessAlreadyExited reports positive kernel evidence that the
// direct child already exited before our cancellation Kill could act.
// A terminated child stays a zombie until the single reaper collects it.
// Kill succeeds against that zombie, so Kill success alone never proves
// causation.
// Only positively observed zombie/dead state or a vanished PID after start
// suppresses the cancellation claim. Zombie/dead state is Linux /proc Z/X,
// Darwin kernel zombie, or POSIX ps STAT containing Z.
// Any undeterminable state returns false. The child stays a live candidate
// and permits a signal attempt. The reaped SIGTERM status check below
// remains the second consistency filter.
func pushDirectProcessAlreadyExited(pid int) bool {
	if pid <= 0 {
		return false
	}
	if exited, definitive := pushDirectProcessKernelState(pid); exited || definitive {
		return exited
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", fmt.Sprintf("%d", pid)).Output()
	if err != nil {
		// A failed ps diagnostic leaves liveness unknown.
		// This unknown rule comes first. Only one completed report counts
		// as verified absence: the silent exit-1 absent-PID report below.
		// A probe that timed out or otherwise failed without an exit
		// status stays unknown.
		// A completed diagnostic terminated by a signal stays unknown.
		// A completed nonzero exit carrying stderr stays unknown.
		// Only a completed report with a plain numeric exit and no output
		// at all can count as verified absence, and only the exit-1 form
		// does.
		if ctx.Err() != nil {
			return false
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false
		}
		if state := exitErr.ProcessState; state != nil {
			if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return false
			}
		}
		if len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
			return false
		}
		if len(bytes.TrimSpace(out)) > 0 {
			return false
		}
		// Only the known unknown-PID report (silent numeric exit 1)
		// counts as verified absence. An arbitrary silent numeric
		// failure (for example exit 42 with no output) proves nothing
		// about the target and stays unknown liveness so a live child
		// still receives its Kill.
		if exitErr.ExitCode() != 1 {
			return false
		}
		return true
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return true
	}
	return strings.Contains(s, "Z")
}

// pushCancelSignalDirectProcess delivers the handshake's distinguishable
// cancellation signal to the direct child: SIGTERM on Unix, so an
// independent SIGKILL reaps a status the cancellation could not have
// produced. It reports whether the signal was delivered without error; a
// failed signal never establishes that cancellation terminated the
// process, while success against an already dead, unreaped zombie proves
// nothing by itself and still needs the probe and status filters.
func pushCancelSignalDirectProcess(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	return proc.Signal(syscall.SIGTERM) == nil
}

// pushProcessKilledBySigTerm reports whether the reaped direct-process
// status agrees with the handshake's own cancellation signal (SIGTERM).
// Only that agreement keeps a cancellation claim: an independent SIGKILL,
// any other signal or any numeric exit proves the cancellation did not
// cause the termination, and a bounded-escalation SIGKILL against a
// signal-resistant child disagrees the same way. An undeterminable status
// preserves the existing claim so a genuine live kill is still reported
// as cancelled. The one stated overlap is an independent SIGTERM racing
// the cancellation SIGTERM: both produce the same status, so that case is
// observationally indistinguishable and keeps the claim.
func pushProcessKilledBySigTerm(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return true
	}
	if status.Exited() {
		return false
	}
	if status.Signaled() {
		return status.Signal() == syscall.SIGTERM
	}
	return true
}

// pushProcessKilledBySigKill reports whether the reaped direct-process
// status is consistent with termination by our own Kill (SIGKILL). A direct
// process that exited independently at the same moment remains a zombie
// until reaped, so Kill can succeed after independent termination; only a
// SIGKILL termination keeps a cancellation claim. Any positively identified
// non-SIGKILL signal or numeric exit proves independent termination. An
// undeterminable status preserves the existing claim so a genuine live kill
// is still reported as cancelled.
func pushProcessKilledBySigKill(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return true
	}
	if status.Exited() {
		return false
	}
	if status.Signaled() {
		return status.Signal() == syscall.SIGKILL
	}
	return true
}
