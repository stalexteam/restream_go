package proc

import (
	"log"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	processTerminate                  = 0x0001
	processSetQuota                   = 0x0100
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	jobOnce   sync.Once
	jobHandle syscall.Handle
)

// newKillOnCloseJob — джоб, який забирає всіх своїх дітей, щойно закриється
// останній його хендл.
func newKillOnCloseJob() (syscall.Handle, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return 0, err
	}
	var info jobExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if r, _, err := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r == 0 {
		_ = syscall.CloseHandle(syscall.Handle(h))
		return 0, err
	}
	return syscall.Handle(h), nil
}

// assignToJob бере вже стартований процес під джоб.
func assignToJob(job syscall.Handle, pid int) error {
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	if r, _, err := procAssignProcessToJobObject.Call(uintptr(job), uintptr(h)); r == 0 {
		return err
	}
	return nil
}

// controllerJob — хендл живе стільки ж, скільки процес контролера, тож його
// смерть забирає всіх дітей.
func controllerJob() syscall.Handle {
	jobOnce.Do(func() {
		h, err := newKillOnCloseJob()
		if err != nil {
			log.Printf("proc: could not create the controller job object: %v", err)
			return
		}
		jobHandle = h
	})
	return jobHandle
}

// ConfigureCmd — no-op: у джоб процес можна взяти лише після CreateProcess.
func ConfigureCmd(*exec.Cmd) {}

// GuardStarted — P1: приписати щойно стартовану дитину до джоба контролера.
func GuardStarted(cmd *exec.Cmd) {
	job := controllerJob()
	if job == 0 || cmd.Process == nil {
		return
	}
	if err := assignToJob(job, cmd.Process.Pid); err != nil {
		log.Printf("proc: could not put pid %d under the controller job: %v", cmd.Process.Pid, err)
	}
}

// TerminateProc — Kill: сигналів Windows не має.
func TerminateProc(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
