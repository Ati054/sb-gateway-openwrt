package sip003

import (
	"errors"
	"os"
	"path/filepath"
)

// This inode stays in place. Unlinking an advisory lock would allow another
// installer to lock a replacement while the first worker still holds its FD.
func acquireInstallLock(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("unsafe plugin installation directory")
	}
	file, err := os.OpenFile(filepath.Join(root, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := verifyInstallLock(root, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func verifyInstallLock(root string, file *os.File) error {
	info, err := file.Stat()
	current, currentErr := os.Lstat(filepath.Join(root, "install.lock"))
	if err != nil || currentErr != nil || !info.Mode().IsRegular() || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 || !os.SameFile(info, current) {
		return errors.New("invalid plugin installation lock")
	}
	return lockPluginInstallFile(file)
}
