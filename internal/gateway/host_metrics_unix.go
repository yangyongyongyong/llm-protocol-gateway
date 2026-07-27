//go:build darwin || linux

package gateway

import "golang.org/x/sys/unix"

// statfsRoot returns total/used bytes of the filesystem backing "/".
func statfsRoot() (total, used uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 {
		return 0, 0, false
	}
	total = st.Blocks * bsize
	free := st.Bavail * bsize
	if total == 0 || free > total {
		return 0, 0, false
	}
	return total, total - free, true
}
