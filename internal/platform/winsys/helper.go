//go:build windows

package winsys

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mustNoErr panics if err is non-nil. Inlined from github.com/sagernet/sing/common.Must.
func mustNoErr(err error) {
	if err != nil {
		panic(err)
	}
}

func CreateDisplayData(name, description string) (FWPM_DISPLAY_DATA0, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return FWPM_DISPLAY_DATA0{}, fmt.Errorf("winsys: 名称含非法字符: %w", err)
	}

	descriptionPtr, err := windows.UTF16PtrFromString(description)
	if err != nil {
		return FWPM_DISPLAY_DATA0{}, fmt.Errorf("winsys: 描述含非法字符: %w", err)
	}

	return FWPM_DISPLAY_DATA0{
		Name:        namePtr,
		Description: descriptionPtr,
	}, nil
}

func GetCurrentProcessAppID() (*FWP_BYTE_BLOB, error) {
	currentFile, err := os.Executable()
	if err != nil {
		return nil, err
	}

	curFilePtr, err := windows.UTF16PtrFromString(currentFile)
	if err != nil {
		return nil, err
	}

	windows.GetCurrentProcessId()

	var appID *FWP_BYTE_BLOB
	err = FwpmGetAppIdFromFileName0(curFilePtr, unsafe.Pointer(&appID))
	if err != nil {
		return nil, err
	}
	return appID, nil
}
