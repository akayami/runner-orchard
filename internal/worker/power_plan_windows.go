package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powerProfile = windows.NewLazySystemDLL("powrprof.dll")
	getScheme    = powerProfile.NewProc("PowerGetActiveScheme")
	setScheme    = powerProfile.NewProc("PowerSetActiveScheme")
	readACIndex  = powerProfile.NewProc("PowerReadACValueIndex")
	writeACIndex = powerProfile.NewProc("PowerWriteACValueIndex")
)

var managedACSettings = [...]struct{ subgroup, setting string }{
	{"{4f971e89-eebd-4455-a8de-9e59040e7347}", "{5ca83367-6e45-459f-a27b-476b1d01c936}"}, // lid action
	{"{238c9fa8-0aad-41ed-83f4-97be242c8f20}", "{29f6c1db-86da-48c5-9fdb-f2b67b1f44da}"}, // sleep timeout
	{"{238c9fa8-0aad-41ed-83f4-97be242c8f20}", "{9d7815a6-7ee4-497e-8888-515a05f02364}"}, // hibernate timeout
}

type powerPlan struct {
	Scheme       string    `json:"scheme"`
	Original     [3]uint32 `json:"original_ac_indexes"`
	Owned        [3]bool   `json:"orchard_owned"`
	Detached     bool      `json:"detached"`
	ApplyPending bool      `json:"apply_pending"`
}

func activePowerScheme() (windows.GUID, error) {
	var scheme *windows.GUID
	result, _, _ := getScheme.Call(0, uintptr(unsafe.Pointer(&scheme)))
	if result != 0 {
		return windows.GUID{}, syscall.Errno(result)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(scheme)))
	return *scheme, nil
}

func readAC(scheme windows.GUID, index int) (uint32, error) {
	setting := managedACSettings[index]
	subgroup, _ := windows.GUIDFromString(setting.subgroup)
	key, _ := windows.GUIDFromString(setting.setting)
	var value uint32
	result, _, _ := readACIndex.Call(0, uintptr(unsafe.Pointer(&scheme)), uintptr(unsafe.Pointer(&subgroup)),
		uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value)))
	if result != 0 {
		return 0, syscall.Errno(result)
	}
	return value, nil
}

func writeAC(scheme windows.GUID, index int, value uint32) error {
	setting := managedACSettings[index]
	subgroup, _ := windows.GUIDFromString(setting.subgroup)
	key, _ := windows.GUIDFromString(setting.setting)
	result, _, _ := writeACIndex.Call(0, uintptr(unsafe.Pointer(&scheme)), uintptr(unsafe.Pointer(&subgroup)),
		uintptr(unsafe.Pointer(&key)), uintptr(value))
	if result != 0 {
		return syscall.Errno(result)
	}
	return nil
}

func savePowerPlan(path string, plan powerPlan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return writeState(path, data)
}

// SnapshotPowerPlan captures only the three opt-in AC settings. The DC
// indexes and the user's global hibernation and display settings are untouched.
func SnapshotPowerPlan(path string) error {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("power snapshot already exists")
	}
	scheme, err := activePowerScheme()
	if err != nil {
		return err
	}
	plan := powerPlan{Scheme: scheme.String()}
	for index := range managedACSettings {
		plan.Original[index], err = readAC(scheme, index)
		if err != nil {
			return fmt.Errorf("cannot snapshot AC power setting %d: %w", index, err)
		}
	}
	return savePowerPlan(path, plan)
}

// ApplyPowerPlan restores only settings still at Orchard's written value.
// If another actor changes the plan or an owned setting, stop managing it.
func ApplyPowerPlan(path string, enabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var plan powerPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	scheme, err := windows.GUIDFromString(plan.Scheme)
	if err != nil {
		return err
	}
	active, err := activePowerScheme()
	if err != nil {
		return err
	}
	if active != scheme && !plan.Detached {
		plan.Detached = true
		if err := savePowerPlan(path, plan); err != nil {
			return err
		}
	}
	for index, original := range plan.Original {
		current, err := readAC(scheme, index)
		if err != nil {
			return err
		}
		expected := original
		if plan.Owned[index] {
			expected = 0
		}
		if current != expected {
			plan.Detached = true
			plan.Owned[index] = false
			if err := savePowerPlan(path, plan); err != nil {
				return err
			}
			continue
		}
		if enabled && !plan.Detached && !plan.Owned[index] && original != 0 {
			plan.Owned[index] = true
			plan.ApplyPending = true
			if err := savePowerPlan(path, plan); err != nil {
				return err
			}
			if err := writeAC(scheme, index, 0); err != nil {
				plan.Owned[index] = false
				_ = savePowerPlan(path, plan)
				return err
			}
		} else if (!enabled || plan.Detached) && plan.Owned[index] {
			plan.ApplyPending = true
			if err := savePowerPlan(path, plan); err != nil {
				return err
			}
			if err := writeAC(scheme, index, original); err != nil {
				return err
			}
			plan.Owned[index] = false
			if err := savePowerPlan(path, plan); err != nil {
				return err
			}
		}
	}
	if plan.ApplyPending && active == scheme {
		current, err := activePowerScheme()
		if err != nil {
			return err
		}
		if current == scheme {
			result, _, _ := setScheme.Call(0, uintptr(unsafe.Pointer(&scheme)))
			if result != 0 {
				return syscall.Errno(result)
			}
			plan.ApplyPending = false
			if err := savePowerPlan(path, plan); err != nil {
				return err
			}
		}
	}
	if plan.Detached && enabled {
		return fmt.Errorf("power plan changed; Orchard released its AC settings")
	}
	return nil
}
