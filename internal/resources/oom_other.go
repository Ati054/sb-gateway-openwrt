//go:build !linux

package resources

func KernelKilledForOOM(int, float64) bool { return false }
