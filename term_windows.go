//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func termWidth() int {
	type coord struct{ X, Y int16 }
	type smallRect struct{ Left, Top, Right, Bottom int16 }
	type consoleScreenBufferInfo struct {
		Size              coord
		CursorPosition    coord
		Attributes        uint16
		Window            smallRect
		MaximumWindowSize coord
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	proc := k32.NewProc("GetConsoleScreenBufferInfo")
	var info consoleScreenBufferInfo
	r, _, _ := proc.Call(os.Stderr.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 100
	}
	w := int(info.Window.Right - info.Window.Left + 1)
	if w <= 0 {
		return 100
	}
	return w
}
