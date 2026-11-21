package worker

import (
	"syscall"
	"unsafe"
)

var getSystemPowerStatus = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

type systemPowerStatus struct {
	acLineStatus       uint8
	batteryFlag        uint8
	batteryLifePercent uint8
	systemStatusFlag   uint8
	batteryLifeTime    uint32
	batteryFullLife    uint32
}

func ACPower() *bool {
	var status systemPowerStatus
	ok, _, _ := getSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if ok != 0 && status.acLineStatus <= 1 {
		ac := status.acLineStatus == 1
		return &ac
	}
	return nil
}
