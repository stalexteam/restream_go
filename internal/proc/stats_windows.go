package proc

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32                 = syscall.NewLazyDLL("kernel32.dll")
	modpsapi                    = syscall.NewLazyDLL("psapi.dll")
	procOpenProcess             = modkernel32.NewProc("OpenProcess")
	procCloseHandle             = modkernel32.NewProc("CloseHandle")
	procGetProcessTimes         = modkernel32.NewProc("GetProcessTimes")
	procGetSystemTimeAsFileTime = modkernel32.NewProc("GetSystemTimeAsFileTime")
	procGetProcessMemoryInfo    = modpsapi.NewProc("GetProcessMemoryInfo")
)

const (
	processQueryLimitedInformation = 0x1000
	processVMRead                  = 0x0010
)

type filetime struct{ lo, hi uint32 }

func (f filetime) ticks100ns() uint64 { return uint64(f.hi)<<32 | uint64(f.lo) }

// processMemoryCounters — PROCESS_MEMORY_COUNTERS (psapi.h), лише поля до
// WorkingSetSize нам потрібні, решта — для правильного розміру структури.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// readRaw — WinAPI-еквівалент /proc: GetProcessTimes+GetProcessMemoryInfo.
func (s *Sampler) readRaw(pid int) (rawSample, bool) {
	h, _, _ := procOpenProcess.Call(
		uintptr(processQueryLimitedInformation|processVMRead), 0, uintptr(pid))
	if h == 0 {
		return rawSample{}, false
	}
	defer procCloseHandle.Call(h)

	var creation, exit, kernel, user filetime
	ret, _, _ := procGetProcessTimes.Call(h,
		uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if ret == 0 {
		return rawSample{}, false
	}

	var mc processMemoryCounters
	mc.cb = uint32(unsafe.Sizeof(mc))
	ret, _, _ = procGetProcessMemoryInfo.Call(h, uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb))
	if ret == 0 {
		return rawSample{}, false
	}

	return rawSample{
		cpuUnits:    int64(kernel.ticks100ns() + user.ticks100ns()),
		unitsPerSec: 1e7,
		rssRaw:      int64(mc.workingSetSize),
		rssDivisor:  1024 * 1024,
		age: func() (float64, bool) {
			var now filetime
			procGetSystemTimeAsFileTime.Call(uintptr(unsafe.Pointer(&now)))
			return float64(now.ticks100ns()-creation.ticks100ns()) / 1e7, true
		},
	}, true
}
