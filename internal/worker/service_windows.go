package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const supervisorService = "RunnerOrchardSupervisor"

type ServiceConfig struct {
	RunnerService string `json:"runner_service"`
	RunnerDir     string `json:"runner_dir"`
	Endpoint      string `json:"lifecycle_endpoint"`
	TokenFile     string `json:"token_file"`
	PauseFile     string `json:"pause_file"`
	PowerPlanFile string `json:"power_plan_file,omitempty"`
}

type windowsListener struct {
	handle windows.Handle
	root   string
}

func (l windowsListener) Running() (bool, error) {
	var state windows.SERVICE_STATUS
	err := windows.QueryServiceStatus(l.handle, &state)
	return state.CurrentState != windows.SERVICE_STOPPED, err
}

func (l windowsListener) Start() error {
	err := windows.StartService(l.handle, 0, nil)
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}

func (l windowsListener) Stop() error {
	var state windows.SERVICE_STATUS
	err := windows.ControlService(l.handle, windows.SERVICE_CONTROL_STOP, &state)
	if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return nil
	}
	return err
}

func (l windowsListener) Busy() (bool, error) {
	var service windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if err := windows.QueryServiceStatusEx(l.handle, windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&service)), uint32(unsafe.Sizeof(service)), &needed); err != nil {
		return false, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	parents := map[uint32]uint32{}
	var workers []uint32
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "Runner.Worker.exe") {
			workers = append(workers, entry.ProcessID)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, err
	}
	path := make([]uint16, 32768)
	for _, pid := range workers {
		for ancestor, depth := parents[pid], 0; ancestor != 0 && depth < 16; ancestor, depth = parents[ancestor], depth+1 {
			if ancestor == service.ProcessId {
				return true, nil
			}
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue // The process ended since the snapshot.
		}
		if err != nil {
			return false, err
		}
		size := uint32(len(path))
		err = windows.QueryFullProcessImageName(handle, 0, &path[0], &size)
		windows.CloseHandle(handle)
		if err != nil {
			return false, err
		}
		// Runner self-update can temporarily run a versioned bin directory.
		dir := filepath.Dir(windows.UTF16ToString(path[:size]))
		if strings.EqualFold(filepath.Dir(dir), l.root) {
			return true, nil
		}
	}
	return false, nil
}

func RunService(config ServiceConfig, collect func(context.Context, func() Status)) error {
	if !strings.HasPrefix(strings.ToLower(config.RunnerService), "actions.runner.") {
		return fmt.Errorf("expected a GitHub Actions Windows runner service")
	}
	token, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return fmt.Errorf("cannot read lifecycle credential")
	}
	control, err := NewRemoteControl(config.Endpoint, strings.TrimSpace(string(token)))
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(config.RunnerService)
	if err != nil {
		return fmt.Errorf("invalid runner service name")
	}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(manager)
	handle, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG|windows.SERVICE_START|windows.SERVICE_STOP)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(handle)
	var size uint32
	if err := windows.QueryServiceConfig(handle, nil, 0, &size); !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size == 0 || size > 8192 {
		return fmt.Errorf("cannot inspect runner service configuration")
	}
	buffer := make([]byte, size)
	service := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buffer[0]))
	if err := windows.QueryServiceConfig(handle, service, size, &size); err != nil {
		return err
	}
	expected := filepath.Join(config.RunnerDir, "bin", "RunnerService.exe")
	executable := strings.Trim(strings.TrimSpace(windows.UTF16PtrToString(service.BinaryPathName)), "\"")
	if service.StartType != windows.SERVICE_DEMAND_START || !strings.EqualFold(executable, expected) {
		return fmt.Errorf("runner must be a demand-start service for the configured directory")
	}
	runner := mgr.Service{Handle: handle, Name: config.RunnerService}
	actions, err := runner.RecoveryActions()
	if err != nil {
		return fmt.Errorf("cannot inspect runner service recovery actions: %w", err)
	}
	for _, action := range actions {
		if action.Type != mgr.NoAction {
			return fmt.Errorf("runner service recovery actions must be disabled")
		}
	}
	listener := windowsListener{handle: handle, root: filepath.Clean(config.RunnerDir)}
	supervisor := Supervisor{Pause: PauseFile(config.PauseFile), Power: ACPower, Remote: control, Listener: listener}
	return svc.Run(supervisorService, serviceHandler{supervisor: supervisor, collect: collect, powerPlan: config.PowerPlanFile})
}

type serviceHandler struct {
	supervisor Supervisor
	collect    func(context.Context, func() Status)
	powerPlan  string
}

func (h serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	control, pipe, err := newLocalControls(h.supervisor.Pause)
	if err != nil {
		return false, uint32(windows.ERROR_OPEN_FAILED)
	}
	defer pipe.Close()
	go control.serve(pipe)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if h.collect != nil {
		go h.collect(ctx, func() Status {
			control.mu.Lock()
			defer control.mu.Unlock()
			return control.status
		})
	}
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	shutdown := false
	for {
		workerStatus, _ := h.supervisor.Step(context.Background(), time.Now())
		if h.powerPlan != "" {
			ac := ACPower()
			manage := !shutdown && workerStatus.PauseUntil == nil && ac != nil && *ac
			if err := ApplyPowerPlan(h.powerPlan, manage); err != nil {
				workerStatus.PowerWarning = "Orchard could not apply or restore AC power settings"
			}
		}
		control.publish(workerStatus)
		if shutdown {
			running, err := h.supervisor.Listener.Running()
			if err == nil && !running {
				return false, 0
			}
			current.CheckPoint++
			status <- current
		}
		select {
		case request, ok := <-requests:
			if !ok {
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				shutdown = true
				ac := false
				h.supervisor.Power = func() *bool { return &ac }
				current = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 35000}
				status <- current
			}
		case <-timer.C:
		case <-control.changed:
		}
	}
}
