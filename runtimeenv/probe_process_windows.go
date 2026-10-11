package runtimeenv

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProbeOwner struct{ job windows.Handle }

func startProbeProcess(cmd *exec.Cmd) (probeProcessOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	owner := &windowsProbeOwner{job: job}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// No candidate instruction executes before assignment to its private job.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	fail := func(err error) (probeProcessOwner, error) {
		cleanupErr := owner.stopAndWait()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.Join(err, cleanupErr)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		return fail(err)
	}
	// Go's exec.Start closes the original primary-thread handle. The suspended
	// child has exactly one thread; reopen only that owned PID's thread to resume.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	threadID := uint32(0)
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
			threadID = entry.ThreadID
			break
		}
	}
	_ = windows.CloseHandle(snapshot)
	if threadID == 0 {
		return fail(fmt.Errorf("owned suspended probe thread unavailable"))
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return fail(err)
	}
	_, err = windows.ResumeThread(thread)
	_ = windows.CloseHandle(thread)
	if err != nil {
		return fail(err)
	}
	return owner, nil
}

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION has the same layout on all Windows
// architectures. ActiveProcesses reaches zero only after every job child exits.
type probeJobAccounting struct {
	totalUserTime, totalKernelTime, thisPeriodUserTime, thisPeriodKernelTime       int64
	totalPageFaultCount, totalProcesses, activeProcesses, totalTerminatedProcesses uint32
}

func (owner *windowsProbeOwner) stopAndWait() error {
	defer windows.CloseHandle(owner.job)
	terminateErr := windows.TerminateJobObject(owner.job, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		accounting := probeJobAccounting{}
		err := windows.QueryInformationJobObject(owner.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if err != nil {
			return errors.Join(terminateErr, err)
		}
		if accounting.activeProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.Join(terminateErr, fmt.Errorf("owned probe job failed to drain"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
