//go:build windows
// +build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"time"
)

func (s *sysstat) handleResize() {
	var lastInfo windows.ConsoleScreenBufferInfo
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		fmt.Println("Error getting console handle:", err)
		return
	}

	start := true
	for {
		var info windows.ConsoleScreenBufferInfo
		err := windows.GetConsoleScreenBufferInfo(handle, &info)
		if err != nil {
			fmt.Println("Error getting console info:", err)
			return
		}

		if info.Window.Right-info.Window.Left+1 != lastInfo.Window.Right-lastInfo.Window.Left+1 ||
			info.Window.Bottom-info.Window.Top+1 != lastInfo.Window.Bottom-lastInfo.Window.Top+1 {

			if start {
				start = false
				lastInfo = info
				time.Sleep(100 * time.Millisecond)
				continue
			}

			s.updateAndWaitResizeEvent()
			s.screenCols = int(info.Window.Right - info.Window.Left + 1)
			s.screenRows = int(info.Window.Bottom - info.Window.Top + 1)

			if s.screenRows < 24 {
				fmt.Println("Screen size is less than the minimum 24x80, exiting")
				return
			}

			switch s.currScreenName {
			case MainScreen:
				switchScreen(s.mainScreen)
			case CPUScreen:
				switchScreen(s.cpuScreen)
			case ProcessScreen:
				switchScreen(s.processScreen)
			case MemoryScreen:
				switchScreen(s.memoryScreen)
			case NetScreen:
				switchScreen(s.netScreen)
			case DiskScreen:
				switchScreen(s.diskScreen)
			case KubernetesScreen:
				switchScreen(s.k8sScreen)
			case DockerScreen:
				switchScreen(s.dockerScreen)
			case K8sLogs:
				switchScreen(s.k8sLogScreen)
			case K8sInteractive:
				switchScreen(s.k8sInteractiveScreen)
			case DockerLogs:
				switchScreen(s.dockerLogScreen)
			case DockerInteractive:
				switchScreen(s.dockerInteractiveScreen)
			}

			lastInfo = info
		}

		time.Sleep(100 * time.Millisecond)
	}
}
