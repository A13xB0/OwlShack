//go:build !windows

package store

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// DiskFreeBytes is the space left for an unprivileged writer where the database lives; ok is false where the platform cannot say.
func (s *Store) DiskFreeBytes() (free uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(s.path), &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
