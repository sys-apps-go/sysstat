//go:build linux
// +build linux

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func (s *sysstat) handleResize() {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGWINCH)
	go s.handleResizeLinux(sigc)
}

func (s *sysstat) handleResizeLinux(c <-chan os.Signal) {
	for sig := range c {
		if sig == syscall.SIGWINCH {
			s.updateAndWaitResizeEvent()
			s.screenCols, s.screenRows = getTerminalSize()
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
		}
	}
}
