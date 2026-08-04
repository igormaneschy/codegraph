//go:build windows

package scip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsProcessPollInterval = 50 * time.Millisecond

// windowsOwnedHandle makes every native handle have one explicit close path.
// The injectable close function is also a deterministic seam for the
// build-tagged lifecycle tests; production handles always use CloseHandle.
type windowsOwnedHandle struct {
	handle  windows.Handle
	closeFn func(windows.Handle) error
	once    sync.Once
	err     error
}

func newWindowsOwnedHandle(handle windows.Handle) *windowsOwnedHandle {
	return &windowsOwnedHandle{handle: handle, closeFn: windows.CloseHandle}
}

func (h *windowsOwnedHandle) close() error {
	if h == nil || h.handle == 0 {
		return nil
	}
	h.once.Do(func() {
		if h.closeFn == nil {
			h.closeFn = windows.CloseHandle
		}
		h.err = h.closeFn(h.handle)
	})
	return h.err
}

type windowsTrackedFile struct {
	file *os.File
	once sync.Once
	err  error
}

func newWindowsTrackedFile(file *os.File) *windowsTrackedFile {
	return &windowsTrackedFile{file: file}
}

func (f *windowsTrackedFile) close() error {
	if f == nil || f.file == nil {
		return nil
	}
	f.once.Do(func() { f.err = f.file.Close() })
	return f.err
}

// windowsLockedWriter preserves os/exec's guarantee that stdout and stderr
// writes are serialized when they target the same non-file writer.
type windowsLockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *windowsLockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// processTree launches SCIP suspended, assigns it to a kill-on-close Job
// Object, and only then resumes its primary thread. The job stays open until
// the process and all inherited-pipe descendants have been reaped or the
// bounded cleanup path has failed closed.
type processTree struct {
	cmd *exec.Cmd
	sys syscall.SysProcAttr
	job *windowsOwnedHandle

	mu              sync.Mutex
	assigned        bool
	resumed         bool
	terminated      bool
	closed          bool
	processWaited   bool
	processRelease  bool
	process         *os.Process
	pidValue        int
	ctx             context.Context
	cleanupDeadline time.Time
	terminationErr  error

	rawProcess *windowsOwnedHandle
	rawThread  *windowsOwnedHandle

	watchStop    chan struct{}
	watchDone    chan struct{}
	watchOnce    sync.Once
	watchStarted bool

	closeOnce sync.Once
	closeErr  error

	inheritedHandles []*windowsOwnedHandle
	childFiles       []*windowsTrackedFile
	parentFiles      []*windowsTrackedFile
	stdHandles       [3]windows.Handle

	ioWait   sync.WaitGroup
	ioDone   chan struct{}
	ioStart  bool
	ioErrMu  sync.Mutex
	ioErr    error
	outputMu sync.Mutex
}

func newProcessTree(cmd *exec.Cmd) (processTreeController, error) {
	if cmd == nil {
		return nil, errors.New("nil SCIP command")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Windows Job Object: %w", err)
	}
	ownedJob := newWindowsOwnedHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return nil, errors.Join(fmt.Errorf("configure kill-on-close Job Object: %w", err), ownedJob.close())
	}

	var sys syscall.SysProcAttr
	if cmd.SysProcAttr != nil {
		sys = *cmd.SysProcAttr
	}
	sys.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr = &sys

	return &processTree{
		cmd:       cmd,
		sys:       sys,
		job:       ownedJob,
		watchStop: make(chan struct{}),
		watchDone: make(chan struct{}),
		ioDone:    make(chan struct{}),
	}, nil
}

func (p *processTree) start(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
	p.startContextWatcher(ctx)

	if err := ctx.Err(); err != nil {
		return p.failStart(err)
	}
	if p.cmd == nil || p.cmd.Path == "" {
		return p.failStart(errors.New("SCIP command has no executable"))
	}
	if p.cmd.Err != nil {
		return p.failStart(p.cmd.Err)
	}
	if len(p.cmd.ExtraFiles) != 0 {
		return p.failStart(errors.New("ExtraFiles are unsupported on Windows"))
	}

	if err := p.prepareIO(); err != nil {
		return p.failStart(fmt.Errorf("prepare SCIP process I/O: %w", err))
	}
	if err := p.createSuspended(); err != nil {
		return p.failStart(fmt.Errorf("create suspended SCIP process: %w", err))
	}
	if err := p.closeLaunchHandles(); err != nil {
		return p.failStart(fmt.Errorf("close SCIP launch handles: %w", err))
	}
	if err := p.attachAndResume(); err != nil {
		return p.failStart(fmt.Errorf("attach and resume SCIP process: %w", err))
	}

	process, err := os.FindProcess(p.pid())
	if err != nil {
		return p.failStart(fmt.Errorf("open SCIP process handle: %w", err))
	}
	p.mu.Lock()
	p.process = process
	p.cmd.Process = process
	p.mu.Unlock()

	if err := p.closeThreadHandle(); err != nil {
		return p.failStart(fmt.Errorf("close SCIP primary thread handle: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return p.failStart(err)
	}
	return nil
}

func (p *processTree) failStart(err error) error {
	return errors.Join(err, p.close())
}

func (p *processTree) wait() error {
	p.mu.Lock()
	process := p.process
	rawProcess := p.rawProcess
	p.mu.Unlock()
	if process == nil || rawProcess == nil {
		return os.ErrProcessDone
	}

	rootErr := p.waitForRootProcess(process, rawProcess)
	ioErr := p.waitForIO()
	if rootErr != nil {
		return rootErr
	}
	return ioErr
}

func (p *processTree) pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pidValue
}

func (p *processTree) startContextWatcher(ctx context.Context) {
	p.mu.Lock()
	if p.watchStarted {
		p.mu.Unlock()
		return
	}
	p.watchStarted = true
	p.mu.Unlock()

	go func() {
		defer close(p.watchDone)
		select {
		case <-ctx.Done():
			p.ensureCleanupDeadline()
			if err := p.terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				p.recordTerminationError(err)
			}
		case <-p.watchStop:
		}
	}()
}

func (p *processTree) stopContextWatcher() {
	p.mu.Lock()
	started := p.watchStarted
	p.mu.Unlock()
	if !started {
		return
	}
	p.watchOnce.Do(func() { close(p.watchStop) })
	<-p.watchDone
}

func (p *processTree) terminate() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return os.ErrProcessDone
	}
	p.terminated = true
	assigned := p.assigned
	job := p.job
	rawProcess := p.rawProcess
	p.mu.Unlock()

	var err error
	if assigned && job != nil {
		err = terminateWindowsJob(job.handle)
	} else if rawProcess != nil {
		err = terminateWindowsProcess(rawProcess.handle)
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		p.recordTerminationError(err)
	}
	return err
}

func (p *processTree) recordTerminationError(err error) {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return
	}
	p.mu.Lock()
	p.terminationErr = errors.Join(p.terminationErr, err)
	p.mu.Unlock()
}

func (p *processTree) ensureCleanupDeadline() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleanupDeadline.IsZero() {
		p.cleanupDeadline = time.Now().Add(scipWaitDelay)
	}
	return p.cleanupDeadline
}

func (p *processTree) cleanupDeadlineValue() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanupDeadline
}

func remainingUntil(deadline time.Time) time.Duration {
	if deadline.IsZero() {
		return scipWaitDelay
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (p *processTree) attachAndResume() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminated {
		return p.contextErrorOr("SCIP process canceled before Job Object assignment")
	}
	if p.rawProcess == nil || p.rawThread == nil || p.job == nil {
		return errors.New("SCIP process handles are unavailable")
	}

	return assignBeforeResume(
		func() error {
			err := windows.AssignProcessToJobObject(p.job.handle, p.rawProcess.handle)
			if err == nil {
				// This flag is set before the resume callback runs. A concurrent
				// cancellation is therefore handled by the Job, never by a
				// post-start PID race.
				p.assigned = true
			}
			return err
		},
		func() error {
			previous, err := windows.ResumeThread(p.rawThread.handle)
			if err != nil {
				return err
			}
			if previous != 1 {
				return fmt.Errorf("unexpected primary-thread suspend count %d", previous)
			}
			p.resumed = true
			return nil
		},
		func() error { return p.abortLocked() },
	)
}

// assignBeforeResume is the critical ordering seam: no successful resume is
// possible unless assignment succeeded, and every failure invokes the
// fail-closed abort path before the error reaches the caller.
func assignBeforeResume(assign, resume, abort func() error) error {
	if err := assign(); err != nil {
		return errors.Join(fmt.Errorf("assign process to Job Object: %w", err), abort())
	}
	if err := resume(); err != nil {
		return errors.Join(fmt.Errorf("resume suspended SCIP process: %w", err), abort())
	}
	return nil
}

func (p *processTree) abortLocked() error {
	if p.assigned && p.job != nil {
		return terminateWindowsJob(p.job.handle)
	}
	if p.rawProcess != nil {
		return terminateWindowsProcess(p.rawProcess.handle)
	}
	return nil
}

func (p *processTree) contextErrorOr(fallback string) error {
	if p.ctx != nil {
		if err := p.ctx.Err(); err != nil {
			return err
		}
	}
	return errors.New(fallback)
}

func (p *processTree) createSuspended() error {
	appName, err := windowsApplicationPath(p.cmd)
	if err != nil {
		return err
	}
	appName16, err := windows.UTF16PtrFromString(appName)
	if err != nil {
		return fmt.Errorf("encode executable path: %w", err)
	}

	sys := p.sys
	args := p.cmd.Args
	if len(args) == 0 {
		args = []string{p.cmd.Path}
	}
	commandLine := windows.ComposeCommandLine(args)
	if sys.CmdLine != "" {
		commandLine = sys.CmdLine
	}
	var commandLine16 *uint16
	if commandLine != "" {
		commandLine16, err = windows.UTF16PtrFromString(commandLine)
		if err != nil {
			return fmt.Errorf("encode SCIP command line: %w", err)
		}
	}

	var currentDir16 *uint16
	if p.cmd.Dir != "" {
		currentDir16, err = windows.UTF16PtrFromString(p.cmd.Dir)
		if err != nil {
			return fmt.Errorf("encode SCIP working directory: %w", err)
		}
	}

	envBlock, err := windowsEnvironmentBlock(p.cmd.Environ())
	if err != nil {
		return fmt.Errorf("build SCIP environment: %w", err)
	}

	attributeList, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return fmt.Errorf("create process attribute list: %w", err)
	}
	defer attributeList.Delete()

	handleValues := p.inheritedHandleValues()
	willInheritHandles := len(handleValues) > 0 && !sys.NoInheritHandles
	if sys.ParentProcess != 0 {
		parent := windows.Handle(sys.ParentProcess)
		if err := attributeList.Update(
			windows.PROC_THREAD_ATTRIBUTE_PARENT_PROCESS,
			unsafe.Pointer(&parent),
			unsafe.Sizeof(parent),
		); err != nil {
			return fmt.Errorf("set parent-process attribute: %w", err)
		}
	}
	if willInheritHandles {
		if err := attributeList.Update(
			windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
			unsafe.Pointer(&handleValues[0]),
			uintptr(len(handleValues))*unsafe.Sizeof(handleValues[0]),
		); err != nil {
			return fmt.Errorf("set inherited-handle attribute: %w", err)
		}
	}

	startup := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  p.stdHandles[0],
			StdOutput: p.stdHandles[1],
			StdErr:    p.stdHandles[2],
		},
		ProcThreadAttributeList: attributeList.List(),
	}
	if sys.HideWindow {
		startup.Flags |= windows.STARTF_USESHOWWINDOW
		startup.ShowWindow = windows.SW_HIDE
	}

	flags := sys.CreationFlags |
		windows.CREATE_SUSPENDED |
		windows.CREATE_UNICODE_ENVIRONMENT |
		windows.EXTENDED_STARTUPINFO_PRESENT
	processInfo := new(windows.ProcessInformation)
	var processErr error
	if sys.Token != 0 {
		processErr = windows.CreateProcessAsUser(
			windows.Token(sys.Token),
			appName16,
			commandLine16,
			windowsSecurityAttributes(sys.ProcessAttributes),
			windowsSecurityAttributes(sys.ThreadAttributes),
			willInheritHandles,
			flags,
			&envBlock[0],
			currentDir16,
			&startup.StartupInfo,
			processInfo,
		)
	} else {
		processErr = windows.CreateProcess(
			appName16,
			commandLine16,
			windowsSecurityAttributes(sys.ProcessAttributes),
			windowsSecurityAttributes(sys.ThreadAttributes),
			willInheritHandles,
			flags,
			&envBlock[0],
			currentDir16,
			&startup.StartupInfo,
			processInfo,
		)
	}
	runtime.KeepAlive(handleValues)
	runtime.KeepAlive(envBlock)
	runtime.KeepAlive(&sys)
	if processErr != nil {
		return processErr
	}

	p.mu.Lock()
	p.rawProcess = newWindowsOwnedHandle(processInfo.Process)
	p.rawThread = newWindowsOwnedHandle(processInfo.Thread)
	p.pidValue = int(processInfo.ProcessId)
	p.mu.Unlock()
	return nil
}

func windowsSecurityAttributes(attr *syscall.SecurityAttributes) *windows.SecurityAttributes {
	if attr == nil {
		return nil
	}
	return (*windows.SecurityAttributes)(unsafe.Pointer(attr))
}

func windowsApplicationPath(cmd *exec.Cmd) (string, error) {
	path := cmd.Path
	if path == "" {
		return "", errors.New("empty executable path")
	}
	if cmd.Dir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(cmd.Dir, path)
	}
	return path, nil
}

func windowsEnvironmentBlock(env []string) ([]uint16, error) {
	env = append([]string(nil), env...)
	if len(env) == 0 {
		return []uint16{0, 0}, nil
	}
	sort.SliceStable(env, func(i, j int) bool {
		return strings.ToUpper(environmentKey(env[i])) < strings.ToUpper(environmentKey(env[j]))
	})

	block := make([]uint16, 0, len(env)+1)
	for _, value := range env {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, errors.New("environment variable contains NUL")
		}
		block = append(block, utf16.Encode([]rune(value))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

func environmentKey(value string) string {
	if i := strings.IndexByte(value, '='); i >= 0 {
		return value[:i]
	}
	return value
}

func (p *processTree) inheritedHandleValues() []windows.Handle {
	values := make([]windows.Handle, 0, len(p.inheritedHandles)+len(p.sys.AdditionalInheritedHandles))
	for _, handle := range p.inheritedHandles {
		if handle != nil && handle.handle != 0 {
			values = append(values, handle.handle)
		}
	}
	for _, handle := range p.sys.AdditionalInheritedHandles {
		if handle != 0 {
			values = append(values, windows.Handle(handle))
		}
	}
	return values
}

func (p *processTree) closeLaunchHandles() error {
	var errs []error
	for _, handle := range p.inheritedHandles {
		if err := handle.close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, file := range p.childFiles {
		if err := file.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *processTree) closeThreadHandle() error {
	if p.rawThread == nil {
		return nil
	}
	return p.rawThread.close()
}

func (p *processTree) prepareIO() error {
	stdin, err := p.prepareInput(p.cmd.Stdin)
	if err != nil {
		return err
	}
	stdout, err := p.prepareOutput(p.cmd.Stdout)
	if err != nil {
		return err
	}
	stderr := stdout
	if !windowsWritersEqual(p.cmd.Stdout, p.cmd.Stderr) {
		stderr, err = p.prepareOutput(p.cmd.Stderr)
		if err != nil {
			return err
		}
	}
	p.stdHandles = [3]windows.Handle{stdin, stdout, stderr}
	p.startIOWaiter()
	return nil
}

func windowsWritersEqual(a, b io.Writer) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

func (p *processTree) prepareInput(reader io.Reader) (windows.Handle, error) {
	if reader == nil {
		file, err := os.Open(os.DevNull)
		if err != nil {
			return 0, err
		}
		p.childFiles = append(p.childFiles, newWindowsTrackedFile(file))
		return p.duplicateFileHandle(file)
	}
	if file, ok := reader.(*os.File); ok {
		return p.duplicateFileHandle(file)
	}

	child, parent, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	parentTracked := newWindowsTrackedFile(parent)
	childTracked := newWindowsTrackedFile(child)
	p.parentFiles = append(p.parentFiles, parentTracked)
	p.childFiles = append(p.childFiles, childTracked)
	p.ioWait.Add(1)
	go func() {
		_, copyErr := io.Copy(parent, reader)
		closeErr := parentTracked.close()
		p.recordIOError(errors.Join(copyErr, closeErr))
		p.ioWait.Done()
	}()
	return p.duplicateFileHandle(child)
}

func (p *processTree) prepareOutput(writer io.Writer) (windows.Handle, error) {
	if writer == nil {
		file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		p.childFiles = append(p.childFiles, newWindowsTrackedFile(file))
		return p.duplicateFileHandle(file)
	}
	if file, ok := writer.(*os.File); ok {
		return p.duplicateFileHandle(file)
	}

	parent, child, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	parentTracked := newWindowsTrackedFile(parent)
	childTracked := newWindowsTrackedFile(child)
	p.parentFiles = append(p.parentFiles, parentTracked)
	p.childFiles = append(p.childFiles, childTracked)
	lockedWriter := &windowsLockedWriter{mu: &p.outputMu, w: writer}
	p.ioWait.Add(1)
	go func() {
		_, copyErr := io.Copy(lockedWriter, parent)
		closeErr := parentTracked.close()
		p.recordIOError(errors.Join(copyErr, closeErr))
		p.ioWait.Done()
	}()
	return p.duplicateFileHandle(child)
}

func (p *processTree) duplicateFileHandle(file *os.File) (windows.Handle, error) {
	if file == nil {
		return 0, errors.New("nil standard-I/O file")
	}
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		windows.Handle(file.Fd()),
		windows.CurrentProcess(),
		&duplicate,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return 0, err
	}
	p.inheritedHandles = append(p.inheritedHandles, newWindowsOwnedHandle(duplicate))
	return duplicate, nil
}

func (p *processTree) startIOWaiter() {
	p.ioStart = true
	go func() {
		p.ioWait.Wait()
		close(p.ioDone)
	}()
}

func (p *processTree) recordIOError(err error) {
	if err == nil {
		return
	}
	p.ioErrMu.Lock()
	p.ioErr = errors.Join(p.ioErr, err)
	p.ioErrMu.Unlock()
}

func (p *processTree) ioError() error {
	p.ioErrMu.Lock()
	defer p.ioErrMu.Unlock()
	return p.ioErr
}

func (p *processTree) waitForRootProcess(process *os.Process, rawProcess *windowsOwnedHandle) error {
	deadline := p.cleanupDeadlineValue()
	for {
		timeout := uint32(windowsProcessPollInterval / time.Millisecond)
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("SCIP process did not terminate within %v after cancellation", scipWaitDelay)
			}
			if remaining < windowsProcessPollInterval {
				timeout = uint32(remaining / time.Millisecond)
				if timeout == 0 {
					timeout = 1
				}
			}
		}

		result, err := windows.WaitForSingleObject(rawProcess.handle, timeout)
		if err != nil {
			return fmt.Errorf("wait for SCIP process: %w", err)
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			state, err := process.Wait()
			p.mu.Lock()
			p.processWaited = err == nil
			if err == nil {
				p.cmd.ProcessState = state
			}
			p.mu.Unlock()
			if err != nil {
				return errors.Join(fmt.Errorf("reap SCIP process: %w", err), p.releaseProcess(process))
			}
			if !state.Success() {
				return &exec.ExitError{ProcessState: state}
			}
			return nil
		case uint32(windows.WAIT_TIMEOUT):
			if p.ctx != nil && p.ctx.Err() != nil && deadline.IsZero() {
				deadline = p.ensureCleanupDeadline()
				if err := p.terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					p.recordTerminationError(err)
				}
			}
		default:
			return fmt.Errorf("unexpected SCIP process wait result %d", result)
		}
	}
}

func (p *processTree) waitForIO() error {
	if !p.ioStart {
		return p.ioError()
	}
	select {
	case <-p.ioDone:
		return p.ioError()
	default:
	}

	deadline := p.ensureCleanupDeadline()
	timer := time.NewTimer(remainingUntil(deadline))
	defer timer.Stop()
	select {
	case <-p.ioDone:
		return p.ioError()
	case <-timer.C:
		killErr := p.terminateForCleanup()
		closeErr := p.closeParentFiles()
		remaining := remainingUntil(deadline)
		if remaining <= 0 {
			return errors.Join(exec.ErrWaitDelay, killErr, closeErr, fmt.Errorf("SCIP I/O did not drain within %v", scipWaitDelay))
		}
		drainTimer := time.NewTimer(remaining)
		defer drainTimer.Stop()
		select {
		case <-p.ioDone:
			return errors.Join(exec.ErrWaitDelay, killErr, closeErr)
		case <-drainTimer.C:
			return errors.Join(
				exec.ErrWaitDelay,
				killErr,
				closeErr,
				fmt.Errorf("SCIP I/O did not drain within %v", scipWaitDelay),
			)
		}
	}
}

func (p *processTree) terminateForCleanup() error {
	p.mu.Lock()
	assigned := p.assigned
	job := p.job
	rawProcess := p.rawProcess
	p.mu.Unlock()

	if assigned && job != nil {
		err := terminateWindowsJob(job.handle)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.recordTerminationError(err)
		}
		return err
	}
	if rawProcess != nil {
		err := terminateWindowsProcess(rawProcess.handle)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.recordTerminationError(err)
		}
		return err
	}
	return nil
}

func terminateWindowsJob(job windows.Handle) error {
	if job == 0 {
		return os.ErrProcessDone
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func terminateWindowsProcess(process windows.Handle) error {
	if process == 0 {
		return os.ErrProcessDone
	}
	if err := windows.TerminateProcess(process, 1); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (p *processTree) closeParentFiles() error {
	var errs []error
	for _, file := range p.parentFiles {
		if err := file.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *processTree) waitForClosedIO(deadline time.Time) error {
	if !p.ioStart {
		return nil
	}
	timer := time.NewTimer(remainingUntil(deadline))
	defer timer.Stop()
	select {
	case <-p.ioDone:
		return nil
	case <-timer.C:
		return fmt.Errorf("SCIP I/O goroutines did not stop within %v", scipWaitDelay)
	}
}

func (p *processTree) waitForContainedProcesses(deadline time.Time) error {
	p.mu.Lock()
	assigned := p.assigned
	job := p.job
	rawProcess := p.rawProcess
	p.mu.Unlock()
	if assigned && job != nil {
		return waitWindowsHandle(job.handle, remainingUntil(deadline))
	}
	if rawProcess != nil {
		return waitWindowsHandle(rawProcess.handle, remainingUntil(deadline))
	}
	return nil
}

func waitWindowsHandle(handle windows.Handle, timeout time.Duration) error {
	result, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	switch result {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("Windows process-tree wait exceeded %v", timeout)
	default:
		return fmt.Errorf("unexpected Windows process-tree wait result %d", result)
	}
}

func (p *processTree) reapProcess() error {
	p.mu.Lock()
	process := p.process
	rawProcess := p.rawProcess
	waited := p.processWaited
	released := p.processRelease
	p.mu.Unlock()
	if process == nil || waited || released {
		return nil
	}
	if rawProcess == nil {
		return p.releaseProcess(process)
	}

	result, err := windows.WaitForSingleObject(rawProcess.handle, 0)
	if err == nil && result == windows.WAIT_OBJECT_0 {
		state, waitErr := process.Wait()
		p.mu.Lock()
		p.processWaited = waitErr == nil
		if waitErr == nil {
			p.cmd.ProcessState = state
		}
		p.mu.Unlock()
		if waitErr != nil {
			return errors.Join(waitErr, p.releaseProcess(process))
		}
		return nil
	}
	if err != nil {
		err = fmt.Errorf("check SCIP process termination: %w", err)
	}
	releaseErr := process.Release()
	p.mu.Lock()
	p.processRelease = true
	p.mu.Unlock()
	return errors.Join(err, releaseErr)
}

func (p *processTree) releaseProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := process.Release()
	p.mu.Lock()
	p.processRelease = true
	p.mu.Unlock()
	return err
}

func (p *processTree) closeResources() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	deadline := p.ensureCleanupDeadline()

	var errs []error
	if err := p.terminateForCleanup(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, err)
	}
	if err := p.waitForContainedProcesses(deadline); err != nil {
		errs = append(errs, err)
	}
	if err := p.reapProcess(); err != nil {
		errs = append(errs, err)
	}
	if err := p.closeParentFiles(); err != nil {
		errs = append(errs, err)
	}
	if err := p.waitForClosedIO(deadline); err != nil {
		errs = append(errs, err)
	}
	if err := p.closeLaunchHandles(); err != nil {
		errs = append(errs, err)
	}
	if err := p.closeThreadHandle(); err != nil {
		errs = append(errs, err)
	}
	if p.rawProcess != nil {
		if err := p.rawProcess.close(); err != nil {
			errs = append(errs, err)
		}
	}
	if p.job != nil {
		if err := p.job.close(); err != nil {
			errs = append(errs, err)
		}
	}
	p.mu.Lock()
	if p.terminationErr != nil {
		errs = append(errs, p.terminationErr)
	}
	p.mu.Unlock()
	return errors.Join(errs...)
}

func (p *processTree) close() error {
	if p == nil {
		return nil
	}
	p.stopContextWatcher()
	p.closeOnce.Do(func() { p.closeErr = p.closeResources() })
	return p.closeErr
}
