//go:build windows

package generation

// Windows is used for development tests; the target OpenWrt build fsyncs
// directory entries after generation and pointer renames.
func syncDirectory(string) error { return nil }
