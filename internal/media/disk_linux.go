package media

import "syscall"

// diskFree -- вільний простір, доступний викликачу (як shutil.disk_usage(...).free).
func diskFree(root string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Frsize), true
}
