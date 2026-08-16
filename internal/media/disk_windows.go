package media

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = modkernel32.NewProc("GetDiskFreeSpaceExW")
)

// diskFree -- вільний простір, доступний викликачу (як shutil.disk_usage(...).free).
func diskFree(root string) (uint64, bool) {
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0, false
	}
	var freeAvail, total, totalFree uint64
	ret, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, false
	}
	return freeAvail, true
}
