//go:build !darwin

package git

// darwinProcessIsZombie is unavailable off Darwin; Linux observes zombies
// through /proc and every other platform falls through to the ps
// diagnostic, so this always reports unknown (false).
func darwinProcessIsZombie(int) bool {
	return false
}
