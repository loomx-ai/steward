package cli

import (
	"syscall"
	"unsafe"
)

// systemLocale returns the Windows user locale, such as "zh-CN".
func systemLocale() string {
	procedure := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	if procedure.Find() != nil {
		return ""
	}
	buffer := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	length, _, _ := procedure.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer)
}
