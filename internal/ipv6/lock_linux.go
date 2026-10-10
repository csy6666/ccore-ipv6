//go:build linux

package ipv6

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type stateLock struct{ file *os.File }

func acquireStateLock(path string) (*stateLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory for lock: %w", err)
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect state lock: %w", err)
	}
	if owner, ok := info.Sys().(*syscall.Stat_t); ok && int(owner.Uid) == os.Geteuid() {
		if err := file.Chmod(0o660); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("protect state lock: %w", err)
		}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock state: %w", err)
	}
	return &stateLock{file: file}, nil
}

func (l *stateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
