package store

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// DiskFreeBytes is the space left for this user where the database lives; ok is false where the platform cannot say.
func (s *Store) DiskFreeBytes() (free uint64, ok bool) {
	dir, err := windows.UTF16PtrFromString(filepath.Dir(s.path))
	if err != nil {
		return 0, false
	}
	if err := windows.GetDiskFreeSpaceEx(dir, &free, nil, nil); err != nil {
		return 0, false
	}
	return free, true
}
