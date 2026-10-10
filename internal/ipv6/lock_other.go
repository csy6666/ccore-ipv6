//go:build !linux

package ipv6

type stateLock struct{}

func acquireStateLock(path string) (*stateLock, error) {
	return &stateLock{}, nil
}

func (l *stateLock) Close() error { return nil }
