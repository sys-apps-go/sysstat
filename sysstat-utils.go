package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/nsf/termbox-go"
	"github.com/shirou/gopsutil/disk"
	gopsutilNet "github.com/shirou/gopsutil/net"
	"github.com/shirou/gopsutil/process"
	"github.com/shirou/gopsutil/v3/docker"
	"golang.org/x/term"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

func (s *sysstat) getProcessList(screenName string) ([]ProcessInfo, error) {
	var processList []ProcessInfo
	var socketCount int

	totalMem := s.params.vMemTotal
	// Get a list of all processes
	processes, err := process.Processes()
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return processList, err
	}

	var diskIO uint64
	for _, p := range processes {
		// Calculate CPU usage over a short interval
		cpuUsage, err := getCpuUsage(p)
		if err != nil {
			continue
		}

		// Get memory usage
		memUsage, err := getMemUsage(p, totalMem)
		if err != nil {
			continue
		}

		// Get memory RSS, MVS
		memInfo, err := p.MemoryInfo()
		if err != nil {
			continue
		}
		cmd, err := p.Cmdline()
		if err != nil {
			continue
		}

		name, err := p.Name()
		if err != nil {
			continue
		}

		if screenName == "disk" {
			ioCounters, err := p.IOCounters()
			if err != nil {
				continue
			}
			diskIO = uint64(ioCounters.ReadBytes+ioCounters.WriteBytes) / (1024 * 1024)
		} else if screenName == "net" {
			connections, err := gopsutilNet.ConnectionsPid("tcp", p.Pid)
			if err != nil {
				continue
			}
			socketCount = len(connections)
		}

		processInfo := ProcessInfo{
			PID:         p.Pid,
			Name:        name,
			Command:     cmd,
			CPUUsage:    cpuUsage,
			MemUsage:    memUsage,
			SocketCount: int32(socketCount),
			RSS:         memInfo.RSS / (1024 * 1024),
			VMS:         memInfo.VMS / (1024 * 1024),
		}

		if screenName == "disk" {
			s.paramsMu.Lock()
			_, exists := s.params.prevDiskIOCounter[p.Pid]
			if !exists {
				s.params.prevDiskIOCounter[p.Pid] = diskIO
			}
			processInfo.RdWrBytes = diskIO - s.params.prevDiskIOCounter[p.Pid]
			s.paramsMu.Unlock()
		}

		processList = append(processList, processInfo)
	}

	return processList, nil
}

func (s *sysstat) printContainerStatusBrief(rowPos int, processCount int, loopCount int) {
	var containersRunning int
	if s.isDockerInstalled && s.isRootUser && (loopCount == 1 || loopCount%10 == 0) {
		dockerStats, err := docker.GetDockerStat()
		if err != nil {
			return
		}

		for _, stat := range dockerStats {
			if !strings.Contains(stat.Status, "Exited") && stat.Running {
				containersRunning++
			}
		}

		containerPortMap, _ := getImagePorts()

		moveCursor(rowPos, 0)

		if containersRunning == 0 {
			fmt.Printf("Total Processes: %v%v%v, Containers Running: %v%v%v", s.valueColor, processCount, Reset, s.valueColor, 0, Reset)
		} else {
			fmt.Printf("Total Processes: %v%v%v, Containers Running: %v%v%v", s.valueColor, processCount, Reset,
				s.valueColor, containersRunning, Reset)
		}
		count := 1
		if s.currScreenName != MainScreen && s.currScreenName != ProcessScreen && s.currScreenName != CPUScreen {
			for _, stat := range dockerStats {
				if !strings.Contains(stat.Status, "Exited") && stat.Running {
					if count == containersRunning {
						fmt.Printf("%v%v%v.Image: %v%v%v, Port: %v%v%v", s.valueColor, count, Reset, s.valueColor, stat.Image, Reset,
							s.valueColor, containerPortMap[stat.Image], Reset)
					} else {
						fmt.Printf("%v%v%v.Image: %v%v%v, Port: %v%v%v, ", s.valueColor, count, Reset, s.valueColor, stat.Image, Reset,
							s.valueColor, containerPortMap[stat.Image], Reset)
					}
					count++
					if count > 10 {
						break
					}
				}
			}
		}
	} else {
		moveCursor(rowPos, 0)
		fmt.Printf("Total Processes: %v%v%v, Containers Running: ", s.valueColor, processCount, Reset)
	}
}

func (s *sysstat) printProcessInfoSortedByCPUUsage(loopCount *int, scrName string) {
	rowPos := s.currRow + 1

	processCount := 10

	// Get a list of all processes
	processList, err := s.getProcessList(CPUScreen)
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}

	// Sort based on CPU usage
	sort.Slice(processList, func(i, j int) bool {
		return processList[i].CPUUsage > processList[j].CPUUsage
	})

	connections, err := gopsutilNet.Connections("all")
	if err != nil {
		return
	}

	dockerInfoRowPos := rowPos

	portUsageProcess := false
	for i := 0; i < processCount; i++ {
		p := processList[i]
		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				portUsageProcess = true
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				portUsageProcess = true
				break
			}
		}
		if portUsageProcess {
			break
		}
	}

	var topDownRow int
	processToPrint := 10
	if s.screenRows <= 30 {
		processToPrint = 5
	}

	topDownRow = s.screenRows - processToPrint

	// Print set of processes with highest CPU usage
	for i := 0; topDownRow < s.screenRows-3 && i < processToPrint; i++ {
		topDownRow = s.screenRows - processToPrint + i
		moveCursor(topDownRow, 0)
		p := processList[i]
		rss := formatMemoryGB(float64(p.RSS))
		vms := formatMemoryGB(float64(p.VMS))
		path := p.Command
		if len(path) > 80 {
			path = path[0:80]
		} else if len(path) == 0 {
			path = p.Name
		}
		port := ""

		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			}
		}
		clearLine()
		if portUsageProcess {
			fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Port: %v%-5v%v Command: %v%-60s%v\n",
				s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
				s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, port, Reset, s.valueColor, path, Reset)
		} else {
			fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
				s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
				s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, path, Reset)
		}
	}

	if *loopCount%60 == 0 {
		s.printDockerContainerInfoBrief(dockerInfoRowPos, len(processList))
	}
	*loopCount = *loopCount + 1

	moveCursor(s.screenRows-2, 0)
	clearLine()

}

func (s *sysstat) printProcessSortedByMemoryUsage(loopCount *int) {
	rowPos := s.currRow + 1
	processCount := 10

	// Get a list of all processes
	processList, err := s.getProcessList("memory")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}
	// Sort based on Memory usage
	sort.Slice(processList, func(i, j int) bool {
		return processList[i].MemUsage > processList[j].MemUsage
	})

	connections, err := gopsutilNet.Connections("all")
	if err != nil {
		return
	}

	dockerInfoRowPos := rowPos

	portUsageProcess := false
	for i := 0; i < processCount; i++ {
		p := processList[i]
		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				portUsageProcess = true
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				portUsageProcess = true
				break
			}
		}
		if portUsageProcess {
			break
		}
	}

	var topDownRow int
	processToPrint := 10
	if s.screenRows <= 30 {
		processToPrint = 5
	}

	topDownRow = s.screenRows - processToPrint

	// Print set of processes with highest CPU usage
	for i := 0; topDownRow < s.screenRows-3 && i < processToPrint; i++ {
		topDownRow = s.screenRows - processToPrint + i
		moveCursor(topDownRow, 0)
		p := processList[i]
		rss := formatMemoryGB(float64(p.RSS))
		vms := formatMemoryGB(float64(p.VMS))
		path := p.Command
		if len(path) > 80 {
			path = path[0:80]
		} else if len(path) == 0 {
			path = p.Name
		}
		port := ""

		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			}
		}
		if portUsageProcess {
			fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Port: %v%-5v%v Command: %v%-60s%v\n",
				s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
				s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, port, Reset, s.valueColor, path, Reset)
		} else {
			fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
				s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
				s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, path, Reset)
		}
	}

	if *loopCount%60 == 0 {
		s.printDockerContainerInfoBrief(dockerInfoRowPos, len(processList))
	}
	*loopCount = *loopCount + 1

}

func (s *sysstat) printProcessSortedBySocketUsage(loopCount *int) {
	processCount := 10
	*loopCount = *loopCount + 1

	// Get a list of all processes
	processList, err := s.getProcessList("net")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}

	// Sort based on Sockets Opened or CPU usage
	sort.Slice(processList, func(i, j int) bool {
		// First, check if both processes have SocketCount > 0
		if processList[i].SocketCount > 0 && processList[j].SocketCount > 0 {
			// If both have SocketCount > 0, sort by SocketCount descending
			if processList[i].SocketCount != processList[j].SocketCount {
				return processList[i].SocketCount > processList[j].SocketCount
			}
			// If SocketCount is the same, sort by CPUUsage descending
			return processList[i].CPUUsage > processList[j].CPUUsage
		}
		// If only one has SocketCount > 0, it should come first
		if processList[i].SocketCount > 0 {
			return true
		}
		if processList[j].SocketCount > 0 {
			return false
		}
		// If both have SocketCount <= 0, sort by CPUUsage descending
		return processList[i].CPUUsage > processList[j].CPUUsage
	})

	connections, err := gopsutilNet.Connections("all")
	if err != nil {
		return
	}

	portUsageProcess := false
	for i := 0; i < processCount; i++ {
		p := processList[i]
		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				portUsageProcess = true
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				portUsageProcess = true
				break
			}
		}
		if portUsageProcess {
			break
		}
	}

	var topDownRow int
	processToPrint := 10
	if s.screenRows <= 30 {
		processToPrint = 5
	}

	topDownRow = s.screenRows - processToPrint
	portMap := detectHTTPListeners()

	// Print set of processes with highest CPU usage
	for i := 0; topDownRow < s.screenRows-3 && i < processToPrint; i++ {
		topDownRow = s.screenRows - processToPrint + i
		moveCursor(topDownRow, 0)
		clearLine()
		p := processList[i]
		path := p.Command
		if len(path) > 80 {
			path = path[0:80]
		} else if len(path) == 0 {
			path = p.Name
		}
		port := ""

		for _, conn := range connections {
			if conn.Pid == p.PID && conn.Raddr.IP == "::" && conn.Raddr.Port == 0 && conn.Laddr.IP != "::" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			} else if conn.Pid == p.PID && conn.Raddr.IP == "0.0.0.0" && conn.Raddr.Port == 0 && conn.Laddr.IP == "0.0.0.0" {
				port = fmt.Sprintf("%v", conn.Laddr.Port)
				break
			}
		}

		if port == "" {
			_, exists := portMap[p.PID]
			if exists {
				port = fmt.Sprintf("%v", portMap[p.PID])
			}
		}
		fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  Sockets: %v%-8v%v Port: %v%-5v%v Command: %v%-60s%v\n",
			s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
			s.valueColor, p.SocketCount, Reset, s.valueColor, port, Reset, s.valueColor, path, Reset)
	}

}

func detectHTTPListeners() map[int32]int {
	cmd := exec.Command("netstat", "-l", "-n", "-t", "-p")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	listeners := make(map[int32]int)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "LISTEN") {
			fields := strings.Fields(line)
			if len(fields) >= 7 {
				addrPort := strings.Split(fields[3], ":")
				port, err := strconv.Atoi(addrPort[len(addrPort)-1])
				if err == nil {
					// Extract PID from the last field
					pidInfo := strings.Split(fields[6], "/")
					if len(pidInfo) >= 1 {
						pid, err := strconv.Atoi(pidInfo[0])
						if err == nil {
							listeners[int32(pid)] = port
						}
					}
				}
			}
		}
	}
	return listeners
}

func (s *sysstat) diskScreen() {
	var err error
	var rootDevice string

	rand.Seed(time.Now().UnixNano())
	s.setScreenFlags("disk")
	s.displayOption = rand.Intn(2)
	defer s.updateExitFlag()

	err = s.readHostInfoParams()
	if err != nil {
		fmt.Printf("Failed to get host info: %v\n", err)
		return
	}

	// Find the mount point for the root filesystem ("/")
	if s.osType == "linux" {
		rootDevice, err = findDeviceForMount("/")
		if err != nil {
			fmt.Printf("Failed to find device for root filesystem: %v\n", err)
			return
		}
	} else {
		rootDevice, _ = findDeviceForMount("C:")
	}

	err = s.readDiskParams(rootDevice)
	if err != nil {
		fmt.Printf("Failed to get disk info: %v\n", err)
		return
	}

	err = s.readMemParams()
	if err != nil {
		fmt.Printf("Failed to get memory info: %v\n", err)
		return
	}

	s.currRow = 1
	s.printMemoryInfo()

	s.printDiskInfo()

	s.maxLength = 30
	loopCount := 0

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	loopStartRow := s.currRow
	for {
		s.currRow = loopStartRow
		s.currRow += 1
		s.printUpdatedSwapInfo()
		s.currRow += 1

		s.printUpdatedDiskInfo()

		s.printSortedDiskUsage(&loopCount)

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
		}
		err = s.readMemParams()
		if err != nil {
			fmt.Printf("Failed to get memory info: %v\n", err)
			return
		}

		err = s.readDiskParams(rootDevice)
		if err != nil {
			fmt.Printf("Failed to get disk info: %v\n", err)
			return
		}

	}
}

// isDockerProcess checks if a process name contains "docker"
func isDockerProcess(name string) bool {
	return name == "docker" || name == "docker-containerd"
}

// getCpuUsage calculates the CPU usage of a process over a short interval
func getCpuUsage(p *process.Process) (float64, error) {
	percent, err := p.CPUPercent()
	if err != nil {
		return 0, err
	}

	return percent, nil
}

func getMemUsage(p *process.Process, totalMem uint64) (float64, error) {
	memInfo, err := p.MemoryInfo()
	if err != nil {
		return 0, err
	}

	// Calculate memory usage as a percentage of total memory
	memUsagePercent := ((float64(memInfo.RSS) * 100) / float64(totalMem))

	return memUsagePercent, nil
}

func printUsageBar(label string, value float64, maxLength int) {
	// Calculate the number of characters for the bar
	barFillLength := int(value / (100.0 / float64(maxLength)))

	// Ensure the length does not exceed the bar length
	if barFillLength > maxLength {
		barFillLength = maxLength
	}

	// Create the bar string
	bar := strings.Repeat("|", barFillLength)

	// Create the padding string
	padding := strings.Repeat(" ", maxLength-barFillLength)

	// Print the usage bar with the percentage
	switch {
	case value < 70:
		fmt.Printf("%s [%v%s%s%v%v%5.1f%%%v]",
			label, White, bar, padding, Reset, White, value, Reset)
	case value >= 70 && value < 90:
		fmt.Printf("%s [%v%s%s%v%v%5.1f%%%v]",
			label, Yellow, bar, padding, Reset, White, value, Reset)
	default:
		fmt.Printf("%s [%v%s%s%v%v%5.1f%%%v]",
			label, Red, bar, padding, Reset, White, value, Reset)
	}
}

func printUsageArrow(label string, value float64, maxLength int) {
	// Calculate the number of characters for the arrow
	arrowFillLength := int(value / (100.0 / float64(maxLength)))

	// Ensure the length does not exceed the maximum length
	if arrowFillLength > maxLength {
		arrowFillLength = maxLength
	}

	// Create the arrow string
	arrows := strings.Repeat("=", arrowFillLength) + ">"

	// Create the padding string
	padding := strings.Repeat(" ", maxLength-arrowFillLength)

	// Print the usage arrow with the percentage
	switch {
	case value < 70:
		fmt.Printf("%s [%s%s%s%s%v%5.1f%%%v]",
			label, White, arrows, padding, "\033[0m", White, value, Reset)
	case value >= 70 && value < 90:
		fmt.Printf("%s [%s%s%s%s%v%5.1f%%%v]",
			label, Yellow, arrows, padding, "\033[0m", White, value, Reset)
	default:
		fmt.Printf("%s [%s%s%s%s%v%5.1f%%%v]",
			label, Red, arrows, padding, "\033[0m", White, value, Reset)
	}
}

func formatPercentage(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func formatMemory(value uint64) string {
	return fmt.Sprintf("%.2fG", float64(value)/(1024*1024*1024))
}

func formatData(bytes uint64) string {
	// Convert bytes to human-readable format (MB, GB, etc.)
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}

func formatTime(duration time.Duration) string {
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

func formatMemoryGB(value float64) string {
	if value >= 1024 {
		return fmt.Sprintf("%.2fG", value/1024)
	}
	return fmt.Sprintf("%.2fM", value)
}

// findDeviceForMount finds the device name (e.g., mmcblk0) associated with a mount point (e.g., "/")
func findDeviceForMount(mountPoint string) (string, error) {
	var (
		rootDevice string
		err        error
	)

	// Get the absolute path of the mount point
	mountPointAbs, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", err
	}

	// Open the mount table
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Scan through the mount table line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPointAbs {
			// Found the mount entry for the specified mount point
			devicePath := fields[0]
			deviceParts := strings.Split(devicePath, "/")
			rootDevice = deviceParts[len(deviceParts)-1] // Extract the device name
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	if rootDevice == "" {
		return "", fmt.Errorf("mount point %s not found in /proc/mounts", mountPoint)
	}

	return rootDevice, nil
}

// moveCursor moves the cursor to the specified row and column position
func moveCursor(row, col int) {
	fmt.Printf("\033[%d;%dH", row, col)
}

func clearLine() {
	fmt.Print("\033[2K") // Clear the entire current line
}

func userInput(input chan<- rune) {
	// Disable input buffering to allow detecting individual key presses
	exec.Command("stty", "-F", "/dev/tty", "cbreak", "min", "1").Run()

	var char rune
	for {
		// Read a single rune from standard input (os.Stdin)
		if _, err := fmt.Scan(&char); err != nil {
			os.Exit(0)
		}
		// Send the read character to the channel
		input <- char
	}
}

func clearScreen() {
	switch runtime.GOOS {
	case "windows":
		// Windows command to clear the screen
		fmt.Print("\x1b[H\x1b[2J")
	default:
		// ANSI escape code to clear the screen
		fmt.Print("\x1b[2J")
	}
	// Move cursor to home position
	fmt.Print("\x1b[H")
}

func saveScreen() string {
	cmd := exec.Command("tput", "smcup")
	cmd.Stdout = os.Stdout
	cmd.Run()
	return ""
}

func restoreScreen(content string) {
	cmd := exec.Command("tput", "rmcup")
	cmd.Stdout = os.Stdout
	cmd.Run()
}

// Function to get a map of container names and their first exposed port
func getContainerPorts() (map[string]int, error) {
	// Run the docker ps command
	cmd := exec.Command("sudo", "docker", "ps")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("error running docker ps: %w", err)
	}

	// Convert the output to a string and split by lines
	output := out.String()
	lines := strings.Split(output, "\n")

	// Regular expression to match port mappings
	portPattern := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+:(\d+)->\d+/tcp`)

	// Map to store container name and its first exposed port
	containerPorts := make(map[string]int)

	for _, line := range lines {
		if strings.Contains(line, "->") {
			// Find the first port match
			match := portPattern.FindStringSubmatch(line)
			if len(match) > 1 {
				// Extract container name (last field in the line)
				fields := strings.Fields(line)
				containerName := fields[len(fields)-1]

				// Convert port to integer
				port, err := strconv.Atoi(match[1])
				if err != nil {
					return nil, fmt.Errorf("error converting port to int: %w", err)
				}

				// Add to map
				containerPorts[containerName] = port
			}
		}
	}

	return containerPorts, nil
}

// Function to get a map of image names and their first exposed port
func getImagePorts() (map[string]int, error) {
	// Run the docker ps command
	cmd := exec.Command("sudo", "docker", "ps")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("error running docker ps: %w", err)
	}

	// Convert the output to a string and split by lines
	output := out.String()
	lines := strings.Split(output, "\n")

	// Regular expression to match port mappings
	portPattern := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+:(\d+)->\d+/tcp`)

	// Map to store image name and its first exposed port
	imagePorts := make(map[string]int)

	for _, line := range lines {
		if strings.Contains(line, "->") {
			// Find the first port match
			match := portPattern.FindStringSubmatch(line)
			if len(match) > 1 {
				// Extract image name (second field in the line)
				fields := strings.Fields(line)
				imageName := fields[1]

				// Convert port to integer
				port, err := strconv.Atoi(match[1])
				if err != nil {
					return nil, fmt.Errorf("error converting port to int: %w", err)
				}

				// Add to map
				imagePorts[imageName] = port
			}
		}
	}

	return imagePorts, nil
}

// isRoot checks if the current process is running as root.
func isRoot() bool {
	return syscall.Geteuid() == 0
}

func (s *sysstat) updateAndWaitNewScreenOption() {
	// Atomically set newScreenOption to true
	atomic.StoreInt32(&s.newScreenOption, 1)

	// Wait until currScreenExited is set to true
	for atomic.LoadInt32(&s.currScreenExited) == 0 {
		time.Sleep(time.Millisecond * 100)
	}

	// Atomically set currScreenExited to false
	atomic.StoreInt32(&s.currScreenExited, 0)
}

func (s *sysstat) updateAndWaitResizeEvent() {
	// Atomically set isScreenResize to true
	atomic.StoreInt32(&s.isScreenResize, 1)

	// Wait until currScreenExited is set to true
	for atomic.LoadInt32(&s.currScreenExited) == 0 {
		time.Sleep(time.Millisecond * 100)
	}

	// Atomically set isScreenResize to false
	atomic.StoreInt32(&s.isScreenResize, 0)
}

func getTerminalSize() (width, height int) {
	if col, row, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return col, row
	}
	return 80, 24 // Default size if GetSize fails
}

func getDockerContainerStatus() []DockerContainerStatus {
	var stats []DockerContainerStatus

	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}|{{.BlockIO}}|{{.PIDs}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		fmt.Println("Error executing command:", err)
		return stats
	}

	output := out.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) < 7 {
			continue
		}

		stat := DockerContainerStatus{
			Name:     fields[0],
			CPUPerc:  fields[1],
			MemUsage: fields[2],
			MemPerc:  fields[3],
			NetIO:    fields[4],
			BlockIO:  fields[5],
			PIDs:     fields[6],
		}

		stats = append(stats, stat)
	}

	return stats
}

func getContainerMemoryUsage() []DockerContainerMemUsage {
	var memUsages []DockerContainerMemUsage
	// Execute the `sudo docker stats` command
	cmd := exec.Command("sudo", "docker", "stats", "--no-stream", "--format", "{{.ID}}   {{.Name}}   {{.CPUPerc}}   {{.MemUsage}}   {{.MemPerc}}   {{.NetIO}}   {{.BlockIO}}   {{.PIDs}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		fmt.Println("Error executing command:", err)
		return memUsages
	}

	// Get the output as a string
	output := out.String()
	lines := strings.Split(output, "\n")

	// Define regex patterns
	memUsagePatternGb := `(\d+\.\d+)GiB / (\d+\.\d+)GiB`
	memUsagePatternMb := `(\d+\.\d+)MiB / (\d+\.\d+)GiB`
	cpuUsagePattern := `(\d+\.\d+)%`
	cpuUsageRegex := regexp.MustCompile(cpuUsagePattern)
	memUsageRegexGb := regexp.MustCompile(memUsagePatternGb)
	memUsageRegexMb := regexp.MustCompile(memUsagePatternMb)

	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// Extract CPU usage
		cpuMatches := cpuUsageRegex.FindStringSubmatch(fields[2])
		cpuPercent := ""
		if len(cpuMatches) > 1 {
			cpuPercent = cpuMatches[1] + "%"
		}

		// Find the memory usage and limit
		var memMatches []string
		foundMb := false
		memMatches = memUsageRegexGb.FindStringSubmatch(fields[3])
		if len(memMatches) == 0 {
			memMatches = memUsageRegexMb.FindStringSubmatch(fields[3])
			if len(memMatches) == 0 {
				continue
			}
			foundMb = true
		}

		memUsageStr := memMatches[1]
		memLimitStr := memMatches[2]

		// Convert to float64
		memUsage, err := strconv.ParseFloat(memUsageStr, 64)
		if err != nil {
			fmt.Println("Error parsing memory usage:", err)
			continue
		}

		memLimit, err := strconv.ParseFloat(memLimitStr, 64)
		if err != nil {
			fmt.Println("Error parsing memory limit:", err)
			continue
		}

		m := DockerContainerMemUsage{
			Name: fields[1], // Use fields[1] for the container name
			CPU:  cpuPercent,
		}

		// Calculate memory usage percentage
		if foundMb {
			memUsagePercentage := ((memUsage * 100) / (memLimit * 1024))
			m.Percent = fmt.Sprintf("%.2f%%", memUsagePercentage)
			m.Used = fmt.Sprintf("%.2f MiB", memUsage)
		} else {
			memUsagePercentage := (memUsage / memLimit) * 100
			m.Percent = fmt.Sprintf("%.2f%%", memUsagePercentage)
			m.Used = fmt.Sprintf("%.2f GiB", memUsage)
		}
		memUsages = append(memUsages, m)
	}
	return memUsages
}

func (s *sysstat) printDockerContainerInfo(containerRow int, loopCount int, processCount int) {
	var containersRunning int
	var dockerStats []docker.CgroupDockerStat
	moveCursor(containerRow, 0)
	if s.isDockerInstalled && s.isRootUser {
		dockerStats, err = docker.GetDockerStat()
		if err != nil {
			return
		}
		for _, stat := range dockerStats {
			if !strings.Contains(stat.Status, "Exited") && stat.Running {
				containersRunning++
			}
		}

		if (s.containersRunning != containersRunning) || (containersRunning > 0 && (loopCount == 1 || loopCount%10 == 0)) {
			s.memUsages = getContainerMemoryUsage()
		}

		containerCountChange := false
		prevContainerCount := 0
		if s.containersRunning != containersRunning {
			containerCountChange = true
			prevContainerCount = s.containersRunning
		}
		s.containersRunning = containersRunning

		containerPortMap, _ := getImagePorts()

		clearLine()
		if containersRunning == 0 {
			moveCursor(containerRow, 0)
			fmt.Printf("Total Processes: %v%v%v", s.valueColor, processCount, Reset)
			moveCursor(containerRow+1, 0)
			fmt.Printf("Containers Running: %v%v%v", s.valueColor, 0, Reset)
		} else {
			moveCursor(containerRow, 0)
			fmt.Printf("Total Processes: %v%v%v", s.valueColor, processCount, Reset)
			moveCursor(containerRow+1, 0)
			fmt.Printf("Containers Running: %v%v%v:", s.valueColor, containersRunning, Reset)
		}

		if s.currScreenName == "memoryDetailed" {
			containerRow += 2
			if containerCountChange {
				containerRowTmp := containerRow
				for i := 0; i < prevContainerCount; i++ {
					moveCursor(containerRowTmp, 0)
					clearLine()
					containerRowTmp++
				}
				containerCountChange = false
			}

			count := 1
			for _, stat := range dockerStats {
				if !strings.Contains(stat.Status, "Exited") && stat.Running {
					if len(s.memUsages) > 0 {
						for _, m := range s.memUsages {
							if m.Name == stat.Name {
								moveCursor(containerRow, 0)
								clearLine()
								fmt.Printf("%v.Image: %v%v%v, CPU used: %v%v%v, Memory used: %v%v%v, Percentage: %v%v%v, Port: %v%v%v\n",
									count, s.valueColor, stat.Image, Reset, s.valueColor, m.CPU, Reset,
									s.valueColor, m.Used, Reset,
									s.valueColor, m.Percent, Reset, s.valueColor, containerPortMap[stat.Image], Reset)
								containerRow++
								break
							}
						}
					} else {
						moveCursor(containerRow, 0)
						clearLine()
						fmt.Printf("%v. Image: %v%v%v, Port: %v%v%v\n", count, s.valueColor, stat.Image, Reset,
							s.valueColor, containerPortMap[stat.Image], Reset)
						containerRow++
					}
					count++
					if count > 10 {
						break
					}
				}
			}
		}
	} else {
		moveCursor(containerRow, 0)
		fmt.Printf("Total Processes: %v%v%v", s.valueColor, processCount, Reset)
		moveCursor(containerRow+1, 0)
		fmt.Printf("Containers Running: ")
	}

}

func (s *sysstat) printSortedDiskUsage(loopCount *int) {
	rowPos := s.currRow + 1

	// Get a list of all processes
	processList, err := s.getProcessList("disk")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}

	// Sort based on Disk usage or CPU usage
	sort.Slice(processList, func(i, j int) bool {
		// First, check if both processes have RdWrBytes > 0
		if processList[i].RdWrBytes > 0 && processList[j].RdWrBytes > 0 {
			// If both have RdWrBytes > 0, sort by RdWrBytes descending
			if processList[i].RdWrBytes != processList[j].RdWrBytes {
				return processList[i].RdWrBytes > processList[j].RdWrBytes
			}
			// If RdWrBytes is the same, sort by CPUUsage descending
			return processList[i].CPUUsage > processList[j].CPUUsage
		}
		// If only one has RdWrBytes > 0, it should come first
		if processList[i].RdWrBytes > 0 {
			return true
		}
		if processList[j].RdWrBytes > 0 {
			return false
		}
		// If both have RdWrBytes <= 0, sort by CPUUsage descending
		return processList[i].CPUUsage > processList[j].CPUUsage
	})

	dockerInfoRowPos := rowPos

	var topDownRow int
	processToPrint := 10
	if s.screenRows <= 30 {
		processToPrint = 5
	}

	topDownRow = s.screenRows - processToPrint

	// Print set of processes with sorted with IO
	for i := 0; topDownRow < s.screenRows-3 && i < processToPrint; i++ {
		topDownRow = s.screenRows - processToPrint + i
		moveCursor(topDownRow, 0)
		p := processList[i]
		rss := formatMemoryGB(float64(p.RSS))
		vms := formatMemoryGB(float64(p.VMS))
		path := p.Command
		if len(path) > 80 {
			path = path[0:80]
		} else if len(path) == 0 {
			path = p.Name
		}

		fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RdWrSizeMB: %v%-8v%v RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
			s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
			s.valueColor, p.RdWrBytes, Reset, s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, path, Reset)
	}

	if *loopCount%60 == 0 {
		s.printDockerContainerInfoBrief(dockerInfoRowPos, len(processList))
	}
	*loopCount = *loopCount + 1

}

func (s *sysstat) printHostInfo() {
	platform := s.params.hostInfo.Platform
	if strings.Contains(platform, "Microsoft Windows") && (len(platform) > (len("Microsoft Windows ") + 2)) {
		platform = platform[:20]
	}

	moveCursor(s.currRow, 0)
	if s.osType == "linux" {
		str := fmt.Sprintf("Host: %v%s%v, Platform: %v%s%v %v%s%v, %v%s%v, OS: %v%s%v, Uptime: %v%s%v, Boot Time: %v%s%v, Virtualization: %v%s%v, Role: %v%s%v\n",
			s.valueColor, s.params.hostInfo.Hostname, Reset,
			s.valueColor, platform, Reset,
			s.valueColor, s.params.hostInfo.PlatformVersion, Reset,
			s.valueColor, s.params.hostInfo.KernelVersion, Reset,
			s.valueColor, s.params.hostInfo.OS, Reset,
			s.valueColor, formatTime(s.params.upTime), Reset,
			s.valueColor, s.params.bootTime.Format("Mon, 02 Jan 2006 15:04:05 MST"), Reset,
			s.valueColor, s.params.hostInfo.VirtualizationSystem, Reset,
			s.valueColor, s.params.hostInfo.VirtualizationRole, Reset)
		fmt.Printf(str)
	} else {
		str := fmt.Sprintf("Host: %v%s%v, Platform: %v%s%v, OS: %v%s%v, Uptime: %v%s%v, Boot Time: %v%s%v, Virtualization: %v%s%v, Role: %v%s%v\n",
			s.valueColor, s.params.hostInfo.Hostname, Reset,
			s.valueColor, platform, Reset,
			s.valueColor, s.params.hostInfo.OS, Reset,
			s.valueColor, formatTime(s.params.upTime), Reset,
			s.valueColor, s.params.bootTime.Format("Mon, 02 Jan 2006 15:04:05 MST"), Reset,
			s.valueColor, s.params.hostInfo.VirtualizationSystem, Reset,
			s.valueColor, s.params.hostInfo.VirtualizationRole, Reset)
		fmt.Printf(str)
	}
	s.currRow += 1
}

func (s *sysstat) printCPUInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("CPU Cores: %v%v%v, Specification: %v%s @ %.2fGHz - %.2f/%.2fGHz%v, Cache Size: %v%vK%v\n",
		s.valueColor, s.params.cpuCores, Reset,
		s.valueColor, s.params.cpuInfo[0].ModelName, s.params.cpuInfo[0].Mhz/1000, 0.00, s.params.cpuInfo[0].Mhz/1000, Reset,
		s.valueColor, s.params.cpuInfo[0].CacheSize, Reset)
	s.currRow += 1
}

func (s *sysstat) printMemoryInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Memory Total: %s%s%s, Buffers: %v%v%v, Cached: %v%v%v\n",
		s.valueColor, formatMemory(s.params.vMem.Total), Reset,
		s.valueColor, formatMemory(s.params.vMem.Buffers), Reset,
		s.valueColor, formatMemory(s.params.vMem.Cached), Reset)
	s.currRow += 1
}

func (s *sysstat) printDiskInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Disk Total: ")
	count := 0
	var str string
	for _, diskInfo := range s.params.pStat {
		usage, err := disk.Usage(diskInfo.Mountpoint)
		if err == nil {
			if diskInfo.Mountpoint == "/" {
				str = fmt.Sprintf("(%s): %s%s%s ", diskInfo.Mountpoint, s.valueColor, formatMemory(usage.Total), Reset)
				count++
			} else if !strings.Contains(diskInfo.Mountpoint, "snap") && !strings.HasPrefix(diskInfo.Mountpoint, "/sys") &&
				!strings.HasPrefix(diskInfo.Mountpoint, "/proc") && !strings.HasPrefix(diskInfo.Mountpoint, "/dev") &&
				!strings.HasPrefix(diskInfo.Mountpoint, "/run") && !strings.Contains(diskInfo.Mountpoint, "docker") &&
				count <= 10 {
				str += fmt.Sprintf("(%s): %s%s%s ", diskInfo.Mountpoint, s.valueColor, formatMemory(usage.Total), Reset)
				count++
			}
		}
	}
	n := len(str) - (count * (len(s.valueColor) + len(Reset)))
	if n < s.screenCols {
		fmt.Printf(str)
	} else {
		fmt.Printf(fmt.Sprintf("%v%v", str[:s.screenCols], Reset))
	}
	s.currRow += 1
}

func (s *sysstat) printNetInfo() {
	moveCursor(s.currRow, 0)
	var ports []string
	var str string
	for _, netInterface := range s.params.iStat {
		for _, addr := range netInterface.Addrs {
			ports = append(ports, fmt.Sprintf("%s (%s)", addr.Addr, netInterface.Name))
		}
	}
	str = fmt.Sprintf("\nNetwork Ports: %s%s%s\n", s.valueColor, strings.Join(ports, ", "), Reset)
	n := len(str) - (1 * (len(s.valueColor) + len(Reset)))
	if n < s.screenCols {
		fmt.Printf(str)
	} else {
		fmt.Printf(fmt.Sprintf("%v%v", str[:s.screenCols], Reset))
	}
	s.currRow += 1
}

func (s *sysstat) printUpdatedCPUTimesInfo() {
	cpuUsage := s.params.cpuPercent[0]
	for _, cpuTime := range s.params.cpuTimes {
		moveCursor(s.currRow, 0)
		fmt.Printf("CPU Status: %v%v%v\n", s.valueColor, cpuTime.String(), Reset)
		s.currRow++
	}
	moveCursor(s.currRow, 0)
	fmt.Printf("CPU Status: Active: %v%s%v  Idle: %v%s%v Average Load: %v%v%v \n",
		s.valueColor, formatPercentage(cpuUsage), Reset, s.valueColor, formatPercentage(100-cpuUsage), Reset,
		s.valueColor, *s.params.avgStat, Reset)

	s.currRow += 2
	moveCursor(s.currRow, 0)

	var coreLabel string
	cores := len(s.params.cpuPercent)

	if s.spacekeyPressed {
		s.spacekeyPressed = false
		s.coreIndexStart += 8
		if s.coreIndexStart > cores {
			s.coreIndexStart = 1
		}
	} else if s.esckeyPressed {
		s.esckeyPressed = false
		s.coreIndexStart = 1
	}

	coreStart := s.coreIndexStart

	for i := 0; i < 4; i++ {
		moveCursor(s.currRow+(i*2), 0)
		clearLine()
	}

	maxCoresPerPage := 8

	moveCursor(s.currRow, 0)
	for i, pct := range s.params.cpuPercent {
		if coreStart < 10 { // Adjust and align
			coreLabel = fmt.Sprintf("%v%v%v%v%v%v:", Green, "CPU Core : ", Reset, Blue, coreStart, Reset)
		} else {
			coreLabel = fmt.Sprintf("%v%v%v%v%v%v:", Green, "CPU Core :", Reset, Blue, coreStart, Reset)
		}

		if s.displayOption == 0 {
			bar := drawBar(pct)
			fmt.Print(coreLabel, bar)
		} else {
			printUsageBar(coreLabel, pct, s.maxLength)
		}

		if i%2 == 0 && pct < 10 {
			fmt.Printf(" ")
		}

		if i%2 != 0 {
			s.currRow += 2
		}

		if i != 0 && i%2 != 0 {
			moveCursor(s.currRow, 0)
		}

		if i%2 == 0 {
			fmt.Printf("     ")
		}

		if (coreStart%maxCoresPerPage) == 0 || coreStart >= cores {
			break
		}

		coreStart += 1
	}

}

func (s *sysstat) printUpdatedCPUInfo() {
	cpuUsage := s.params.cpuPercent[0]

	moveCursor(s.currRow, 0)
	clearLine()
	moveCursor(s.currRow+1, 0)
	clearLine()
	moveCursor(s.currRow, 0)
	fmt.Printf("CPU Status: Active: %v%s%v, Idle: %v%s%v, Avg Load: %v%v%v,  ",
		s.valueColor, formatPercentage(cpuUsage), Reset, s.valueColor, formatPercentage(100-cpuUsage), Reset,
		s.valueColor, *s.params.avgStat, Reset)

	var coreLabel string
	for _, pct := range s.params.cpuPercent {
		coreLabel = fmt.Sprintf("CPU:")
		if s.displayOption == 0 {
			printUsageArrow(coreLabel, pct, s.maxLength)
		} else {
			printUsageBar(coreLabel, pct, s.maxLength)
		}
		break
	}
	s.currRow += 1
}

func (s *sysstat) printUpdatedMemInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Memory Available: %v%v%v, Free: %v%v%v, Used: %v%v%v, Used Percent: %v%.2f%v, Buffers: %v%v%v, Cached: %v%v%v",
		s.valueColor, formatMemory(s.params.vMem.Available), Reset, s.valueColor, formatMemory(s.params.vMem.Free), Reset,
		s.valueColor, formatMemory(s.params.vMem.Used), Reset, s.valueColor, s.params.vMem.UsedPercent, Reset,
		s.valueColor, formatMemory(s.params.vMem.Buffers), Reset,
		s.valueColor, formatMemory(s.params.vMem.Cached), Reset)

	s.currRow += 1
	moveCursor(s.currRow, 0)
	if s.displayOption == 0 {
		printUsageArrow("MEM", s.params.vMem.UsedPercent, s.maxLength)
	} else {
		printUsageBar("MEM", s.params.vMem.UsedPercent, s.maxLength)
	}

	s.currRow += 2
	moveCursor(s.currRow, 0)
	fmt.Printf("Swap Space Total: %v%v%v, Free: %v%v%v, Used: %v%v%v, Used Percent: %v%.2f%v",
		s.valueColor, formatMemory(s.params.sMem.Total), Reset, s.valueColor, formatMemory(s.params.sMem.Free), Reset,
		s.valueColor, formatMemory(s.params.sMem.Used), Reset, s.valueColor, s.params.sMem.UsedPercent, Reset)

	s.currRow += 1
	moveCursor(s.currRow, 0)
	if s.displayOption == 0 {
		printUsageArrow("SWAP", s.params.sMem.UsedPercent, s.maxLength)
	} else {
		printUsageBar("SWAP", s.params.sMem.UsedPercent, s.maxLength)
	}
	s.currRow += 1
}

func (s *sysstat) printUpdatedSwapInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Swap Space Total: %v%v%v, Free: %v%v%v, Used: %v%v%v, Used Percent: %v%.2f%v",
		s.valueColor, formatMemory(s.params.sMem.Total), Reset, s.valueColor, formatMemory(s.params.sMem.Free), Reset,
		s.valueColor, formatMemory(s.params.sMem.Used), Reset, s.valueColor, s.params.sMem.UsedPercent, Reset)

	s.currRow += 1
	moveCursor(s.currRow, 0)
	if s.displayOption == 0 {
		printUsageArrow("SWAP", s.params.sMem.UsedPercent, s.maxLength)
	} else {
		printUsageBar("SWAP", s.params.sMem.UsedPercent, s.maxLength)
	}
	s.currRow += 1
}

func (s *sysstat) printUpdatedDiskInfo() {
	currReadCount := uint64(0)
	currWriteCount := uint64(0)
	iopsInProgress := uint64(0)
	readBytes := uint64(0)
	writeBytes := uint64(0)

	for _, counters := range s.params.ioCounters {
		currReadCount = counters.ReadCount - s.params.prevReadCount
		currWriteCount = counters.WriteCount - s.params.prevWriteCount
		iopsInProgress = counters.IopsInProgress
		readBytes = counters.ReadBytes - s.params.prevReadBytes
		writeBytes = counters.WriteBytes - s.params.prevWriteBytes
		break
	}

	moveCursor(s.currRow, 0)
	clearLine()
	fmt.Printf("Disk Space(/): Free: %v%v%v, Used: %v%v%v, Used Percent: %v%.2f%v, ReadCount: %v%v%v, WriteCount: %v%v%v, IopsInProgress: %v%v%v, ReadBytes: %v%v%v, WriteBytes: %v%v%v",
		s.valueColor, formatMemory(s.params.uStat.Free), Reset,
		s.valueColor, formatMemory(s.params.uStat.Used), Reset, s.valueColor, s.params.uStat.UsedPercent, Reset,
		s.valueColor, currReadCount, Reset, s.valueColor, currWriteCount, Reset,
		s.valueColor, iopsInProgress, Reset,
		s.valueColor, formatMemoryGB(float64(readBytes)/float64(1024*1024)), Reset, s.valueColor, formatMemoryGB(float64(writeBytes)/float64(1024*1024)), Reset)

	s.currRow += 1
	moveCursor(s.currRow, 0)
	if s.displayOption == 0 {
		printUsageArrow("Disk(/)", s.params.uStat.UsedPercent, s.maxLength)
	} else {
		printUsageBar("Disk(/)", s.params.uStat.UsedPercent, s.maxLength)
	}
	s.currRow += 1
}

func (s *sysstat) printUpdatedNetInfo() {
	var str string

	moveCursor(s.currRow, 0)

	if len(s.params.netIOCounters) > 0 {
		for _, netIO := range s.params.netIOCounters {
			name := netIO.Name
			if name == "lo" || strings.HasPrefix(name, "wlo") || strings.HasPrefix(name, "br-") {
				continue
			}
			str += fmt.Sprintf("NET: [%v%s%v]  ", s.valueColor, netIO.Name, Reset)

			str += fmt.Sprintf("In: %v%v%v  Out: %v%v%v, ",
				s.valueColor, formatData(netIO.BytesRecv), Reset,
				s.valueColor, formatData(netIO.BytesSent), Reset)
		}

	}

	n := len(str)
	if n < (s.screenCols * 4) {
		fmt.Printf(str)
	} else {
		fmt.Printf(fmt.Sprintf("%v%v", str[:s.screenCols*4], Reset))
	}
	s.currRow += 1
}

func (s *sysstat) printNetInfoDetailed() {
	moveCursor(s.currRow, 0)
	interfaces, err := gopsutilNet.Interfaces()
	if err != nil {
		fmt.Printf("Error getting network interfaces: %v\n", err)
		return
	}

	ioCounters, err := gopsutilNet.IOCounters(true)
	if err != nil {
		fmt.Printf("Error getting IO counters: %v\n", err)
		return
	}

	for _, iface := range interfaces {
		if strings.Contains(iface.Name, "lo") || strings.HasPrefix(iface.Name, "br-") {
			continue
		}
		var ips []string
		for _, addr := range iface.Addrs {
			ips = append(ips, addr.Addr)
		}

		var inBytes, outBytes uint64
		for _, counter := range ioCounters {
			if counter.Name == iface.Name {
				inBytes = counter.BytesRecv
				outBytes = counter.BytesSent
				break
			}
		}

		fmt.Printf("Name: %v%v%v, MAC: %v%v%v, MTU: %v%v%v, IPs: %v%v%v, In: %v%v%v, Out: %v%v%v\r\n",
			s.valueColor, iface.Name, Reset,
			s.valueColor, iface.HardwareAddr, Reset,
			s.valueColor, iface.MTU, Reset,
			s.valueColor, strings.Join(ips, ", "), Reset,
			s.valueColor, formatData(inBytes), Reset,
			s.valueColor, formatData(outBytes), Reset)
	}
}

func (s *sysstat) k8sScreen() {
	var err error
	var currIndex int
	s.setScreenFlags("kubernetes")
	defer s.updateExitFlag()

	// Get the KUBECONFIG environment variable or default to the user's home directory
	if s.kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Printf("Error getting home directory: %s", err.Error())
			return
		}
		s.kubeconfig = filepath.Join(homeDir, ".kube", "config")
	}

	// Build the Kubernetes config from the kubeconfig file
	config, err := clientcmd.BuildConfigFromFlags("", s.kubeconfig)
	if err != nil {
		fmt.Printf("Error building kubeconfig: %s", err.Error())
		return
	}

	s.clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating Kubernetes client: %s", err.Error())
		return
	}

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type pod number and l for log contents or i for interactive mode or s for pod description, space for next page, k for kubernetes refresh, r for main screen, q to quit")
	// Get nodes
	nodes, err := s.clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing nodes: %s", err.Error())
		return
	}
	count := 0
	for _, node := range nodes.Items {
		if !strings.Contains(node.Name, "control") {
			count++
		}
	}
	workerNodes := count

	moveCursor(1, 0)
	fmt.Printf("Number of Worker Nodes: %v%v%v: ", s.valueColor, workerNodes, Reset)
	count = 1
	for _, node := range nodes.Items {
		if !strings.Contains(node.Name, "control") {
			if count != workerNodes {
				fmt.Printf("%v.Name: %v%v%v, IP: %v%v%v, ", count, s.valueColor, node.Name, Reset,
					s.valueColor, node.Status.Addresses[0].Address, Reset)
			} else {
				fmt.Printf("%v.Name: %v%v%v, IP: %v%v%v\n", count, s.valueColor, node.Name, Reset,
					s.valueColor, node.Status.Addresses[0].Address, Reset)
			}
			count++
		}
	}
	fmt.Println()

	// Get namespaces
	namespaces, err := s.clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing namespaces: %s", err.Error())
		return
	}
	moveCursor(4, 0)
	fmt.Printf("Number of Namespaces: %v%v%v: ", s.valueColor, len(namespaces.Items), Reset)
	for i, ns := range namespaces.Items {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%v%v%v", s.valueColor, ns.Name, Reset)
	}
	fmt.Println()

	// Get pods
	pods, err := s.clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing pods: %s", err.Error())
		return
	}

	containerCount := 0
	podCount := 0
	s.cInfo = make([]containerInfo, 0)
	for _, pod := range pods.Items {
		if pod.Namespace != "kube-system" && pod.Namespace != "local-path-storage" {
			podCount++
		} else {
			continue
		}
		for _, container := range pod.Status.ContainerStatuses {
			containerCount++
			c := containerInfo{
				containerName:      container.Name,
				containerNamespace: pod.Namespace,
				containerPodName:   pod.Name,
				containerPodIP:     pod.Status.PodIP,
				containerNodeName:  pod.Spec.NodeName,
			}
			s.cInfo = append(s.cInfo, c)
		}
	}

	sort.Slice(s.cInfo, func(i, j int) bool {
		return s.cInfo[i].containerPodName < s.cInfo[j].containerPodName
	})

	i := 0
	for i < len(s.cInfo) {
		c := s.cInfo[i]
		c.containerIndex = i + 1
		s.cInfo[i] = c
		i++
	}

	s.containerCount = containerCount

	moveCursor(6, 0)
	fmt.Printf("Number of Pods: %v%v%v: User Pods: %v%v%v", s.valueColor, len(pods.Items), Reset, s.valueColor, podCount, Reset)

	s.printContainerChart(7, 0, &currIndex)

	ingresses, err := s.clientset.NetworkingV1().Ingresses("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing Ingresses: %s", err.Error())
		return
	}

	moveCursor(s.screenRows-25, 0)
	fmt.Printf("Ingresses: %v%v%v: ", s.valueColor, len(ingresses.Items), Reset)
	services, err := s.clientset.CoreV1().Services("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing services: %s", err.Error())
		return
	}
	for _, ing := range ingresses.Items {
		if ing.Namespace != "kube-system" && ing.Namespace != "local-path-storage" {
			fmt.Printf("%v%v%v (Namespace: %v%v%v) ",
				s.valueColor, ing.Name, Reset,
				s.valueColor, ing.Namespace, Reset)
			break
		}
	}

	moveCursor(s.screenRows-23, 0)
	fmt.Printf("Services: %v%v%v: ", s.valueColor, len(services.Items), Reset)
	for _, svc := range services.Items {
		if !strings.Contains(svc.Namespace, "kube-system") {
			fmt.Printf("%v%v%v (Namespace: %v%v%v, ClusterIP: %v%v%v", s.valueColor, svc.Name, Reset,
				s.valueColor, svc.Namespace, Reset, s.valueColor, svc.Spec.ClusterIP, Reset)

			// Check for LoadBalancer type and print its status
			if svc.Spec.Type == v1.ServiceTypeLoadBalancer {
				if len(svc.Status.LoadBalancer.Ingress) > 0 {
					// Retrieve either IP or Hostname from LoadBalancer Ingress
					ingress := svc.Status.LoadBalancer.Ingress[0]
					if ingress.IP != "" {
						fmt.Printf(", LoadBalancerIP: %v%v%v", s.valueColor, ingress.IP, Reset)
					} else if ingress.Hostname != "" {
						fmt.Printf(", LoadBalancerHostname: %v%v%v", s.valueColor, ingress.Hostname, Reset)
					} else {
						fmt.Printf(", LoadBalancerIP: %vPending%v", s.valueColor, Reset)
					}
				} else {
					fmt.Printf(", LoadBalancerIP: %vNot Assigned%v", s.valueColor, Reset)
				}
			}
			// Print service ports
			for _, port := range svc.Spec.Ports {
				fmt.Printf(", Port: %v%v%v/%v%v%v", s.valueColor, port.Port, Reset,
					s.valueColor, port.Protocol, Reset)
			}
			fmt.Print(")")
		}
		break
	}

	totalLabelCount := 0
	for _, pod := range pods.Items {
		totalLabelCount += len(pod.Labels)
	}
	moveCursor(s.screenRows-21, 0)
	fmt.Printf("Labels: %v%v%v: ", s.valueColor, totalLabelCount, Reset)

	for _, pod := range pods.Items {
		if pod.Namespace != "kube-system" && pod.Namespace != "local-path-storage" {
			for key, value := range pod.Labels {
				fmt.Printf("Key: %v%v%v, Value: ", s.valueColor, key, Reset)
				fmt.Printf("%v%v%v ", s.valueColor, value, Reset)
				break
			}
		}
		break
	}

	deployments, err := s.clientset.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing Deployments: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-19, 0)
	fmt.Printf("Deployments: %v%v%v: ", s.valueColor, len(deployments.Items), Reset)

	for _, deploy := range deployments.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Replicas: %v%v/%v%v)",
			s.valueColor, deploy.Name, Reset,
			s.valueColor, deploy.Namespace, Reset,
			s.valueColor, deploy.Status.ReadyReplicas, deploy.Status.Replicas, Reset)
		break
	}

	cronJobs, err := s.clientset.BatchV1().CronJobs("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing CronJobs: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-17, 0)
	fmt.Printf("CronJobs: %v%v%v: ", s.valueColor, len(cronJobs.Items), Reset)

	for _, cj := range cronJobs.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Schedule: %v%v%v)",
			s.valueColor, cj.Name, Reset,
			s.valueColor, cj.Namespace, Reset,
			s.valueColor, cj.Spec.Schedule, Reset)
		break
	}
	// Get Jobs
	jobs, err := s.clientset.BatchV1().Jobs("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing Jobs: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-15, 0)
	fmt.Printf("Jobs: %v%v%v: ", s.valueColor, len(jobs.Items), Reset)

	for _, job := range jobs.Items {
		completions := "N/A"
		if job.Spec.Completions != nil {
			completions = fmt.Sprintf("%d", *job.Spec.Completions)
		}
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Completions: %v%v/%s%v)",
			s.valueColor, job.Name, Reset,
			s.valueColor, job.Namespace, Reset,
			s.valueColor, job.Status.Succeeded, completions, Reset)
		break
	}
	// Get PersistentVolumeClaims
	pvcs, err := s.clientset.CoreV1().PersistentVolumeClaims("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing PersistentVolumeClaims: %s", err.Error())
		return
	}

	moveCursor(s.screenRows-13, 0)
	fmt.Printf("PersistentVolumeClaims: %v%v%v: ", s.valueColor, len(pvcs.Items), Reset)
	for _, pvc := range pvcs.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Status: %v%v%v", s.valueColor, pvc.Name, Reset,
			s.valueColor, pvc.Namespace, Reset, s.valueColor, pvc.Status.Phase, Reset)
		fmt.Print(")")
		break
	}

	// Get StorageClasses
	storageClasses, err := s.clientset.StorageV1().StorageClasses().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing StorageClasses: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-11, 0)
	fmt.Printf("StorageClasses: %v%v%v: ", s.valueColor, len(storageClasses.Items), Reset)
	for _, sc := range storageClasses.Items {
		fmt.Printf("%v%v%v", s.valueColor, sc.Name, Reset)
		break
	}

	// Get ConfigMaps
	configMaps, err := s.clientset.CoreV1().ConfigMaps("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing ConfigMaps: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-9, 0)
	fmt.Printf("ConfigMaps: %v%v%v: ", s.valueColor, len(configMaps.Items), Reset)

	for _, cm := range configMaps.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v)",
			s.valueColor, cm.Name, Reset,
			s.valueColor, cm.Namespace, Reset)
		break
	}

	// Get StatefulSets
	statefulSets, err := s.clientset.AppsV1().StatefulSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing StatefulSets: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-7, 0)
	fmt.Printf("StatefulSets: %v%v%v: ", s.valueColor, len(statefulSets.Items), Reset)
	for _, sts := range statefulSets.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Replicas: %v%v/%v%v)",
			s.valueColor, sts.Name, Reset,
			s.valueColor, sts.Namespace, Reset,
			s.valueColor, sts.Status.ReadyReplicas, sts.Status.Replicas, Reset)
		break
	}

	replicaSets, err := s.clientset.AppsV1().ReplicaSets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing ReplicaSets: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-5, 0)
	fmt.Printf("ReplicaSets: %v%v%v: ", s.valueColor, len(replicaSets.Items), Reset)

	for _, rs := range replicaSets.Items {
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Replicas: %v%v/%v%v)",
			s.valueColor, rs.Name, Reset,
			s.valueColor, rs.Namespace, Reset,
			s.valueColor, rs.Status.ReadyReplicas, rs.Status.Replicas, Reset)
		break
	}
	for {
		moveCursor(s.screenRows-1, 0)
		for i := 0; i < 10; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.nextPageOption {
				s.nextPageOption = false
				s.printContainerChart(7, 0, &currIndex)
				break
			}
			if s.checkAndUpdate() {
				return
			}
		}

	}
}

func (s *sysstat) printContainerChart(row, col int, currIndex *int) {
	rowPos := row + 1
	n := len(s.cInfo)
	currCount := 0
	printCount := 0
	for i := *currIndex; i < n; i++ {
		currCount++
		cInfo := s.cInfo[i]
		moveCursor(rowPos, col)
		clearLine()
		moveCursor(rowPos, col)
		fmt.Printf("%v%2v%v. Pod Name: %v%v%v, Container: %v%v%v, Namespace: %v%v%v, PodIP: %v%v%v, Container Node: %v%v%v",
			s.valueColor, cInfo.containerIndex, Reset, s.valueColor, cInfo.containerPodName, Reset,
			s.valueColor, cInfo.containerName, Reset, s.valueColor, cInfo.containerNamespace, Reset,
			s.valueColor, cInfo.containerPodIP, Reset, s.valueColor, cInfo.containerNodeName, Reset)
		printCount++
		rowPos++
		if currCount >= maxContainerList {
			break
		}
	}

	if currCount <= maxContainerList {
		for i := currCount; i <= maxContainerList; i++ {
			moveCursor(rowPos, col)
			clearLine()
			rowPos++
		}
		if *currIndex+currCount >= n {
			*currIndex = 0
		} else {
			*currIndex += printCount
		}
	} else {
		*currIndex += printCount
	}
}

func (s *sysstat) k8sLogScreen() {
	s.setScreenFlags(K8sLogs)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.readPodContainerLogWithAPI(s.cInfo[s.currOption-1].containerNamespace, s.cInfo[s.currOption-1].containerPodName, s.cInfo[s.currOption-1].containerName)
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) k8sPodDescriptionScreen() {
	s.setScreenFlags(K8sPodDescription)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.printPodDescription(s.cInfo[s.currOption-1].containerNamespace, s.cInfo[s.currOption-1].containerPodName)
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) dockerLogScreen() {
	s.setScreenFlags(DockerLogs)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.readDockerContainerLogWithAPI(s.containerIDs[s.currOption-1])
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) docketContainerInspectionScreen() {
	s.setScreenFlags(DockerInspection)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.printDockerInspectionContents(s.containerIDs[s.currOption-1])
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) k8sInteractiveScreen() {
	s.setScreenFlags(K8sInteractive)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.enterPodContainerShell(s.cInfo[s.currOption-1].containerNamespace, s.cInfo[s.currOption-1].containerPodName, s.cInfo[s.currOption-1].containerName)
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) dockerInteractiveScreen() {
	s.setScreenFlags(DockerInteractive)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	moveCursor(1, 0)
	s.enterDockerContainerShell(s.containerIDs[s.currOption-1])
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func checkDockerInstalled() bool {
	// Try to execute the "docker --version" command
	cmd := exec.Command("docker", "--version")
	err := cmd.Run()

	// If the command runs successfully, Docker is installed
	if err == nil {
		return true
	}

	// If there's an error, Docker is not installed
	return false
}

func (s *sysstat) printDockerContainerInfoBrief(rowPos int, processCount int) {
	moveCursor(rowPos, 0)
	clearLine()
	if s.isDockerInstalled && s.isRootUser {
		numContainers, totalCPU, totalMemUsed, totalMemPerc, _ := getTotalContainerUsage()
		s.containerCount = numContainers
		if numContainers > 0 {
			if s.currScreenName == CPUScreen || s.currScreenName == MemoryScreen {
				fmt.Printf("Docker Container Info: Process Running: %v%v%v, Containers Running: %v%v%v, Total CPU Usage: %v%.2f%%%v, Total Memory Usage:  %v%.2fM%v, Used Percentage: %v%.2f%%%v\n",
					s.valueColor, processCount, Reset,
					s.valueColor, numContainers, Reset, s.valueColor, totalCPU, Reset, s.valueColor, totalMemUsed, Reset,
					s.valueColor, totalMemPerc, Reset)
			} else {
				fmt.Printf("Docker Container Info: Containers Running: %v%v%v, Total CPU Usage: %v%.2f%%%v, Total Memory Usage:  %v%.2fM%v, Used Percentage: %v%.2f%%%v\n",
					s.valueColor, numContainers, Reset, s.valueColor, totalCPU, Reset, s.valueColor, totalMemUsed, Reset,
					s.valueColor, totalMemPerc, Reset)
			}
		} else {
			if s.currScreenName == CPUScreen || s.currScreenName == MemoryScreen {
				fmt.Printf("Docker Container Info: Process Running: %v%v%v, Containers Running: %v%v%v\n",
					s.valueColor, processCount, Reset,
					s.valueColor, 0, Reset)
			} else {
				fmt.Printf("Docker Container Info: Containers Running: %v%v%v\n",
					s.valueColor, 0, Reset)
			}
		}
	} else {
		if s.currScreenName == CPUScreen || s.currScreenName == MemoryScreen || s.currScreenName == MainScreen {
			fmt.Printf("Process Running: %v%v%v, Containers Running: \n",
				s.valueColor, processCount, Reset)
		} else {
			fmt.Printf("Containers Running: \n")
		}
	}
}

func (s *sysstat) printDockerContainerInfoDetailed(rowPos int) error {
	s.containerIDs = make([]string, 0)

	// Create Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	// List containers
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{})
	if err != nil {
		return err
	}

	numContainers := len(containers)
	s.containerCount = numContainers
	if s.params.prevContainers != numContainers {
		clearScreen()
		s.params.prevContainers = numContainers
	}
	off := 0
	moveCursor(rowPos+off, 0)
	clearLine()
	if s.isDockerInstalled && s.isRootUser && numContainers > 0 {
		_, totalCPU, totalMemUsed, totalMemPerc, _ := getTotalContainerUsage()
		fmt.Printf("Containers Running: %v%v%v, Total CPU Usage: %v%.2f%%%v, Total Memory Usage:  %v%.2fM%v, Used Percentage: %v%.2f%%%v\n",
			s.valueColor, numContainers, Reset, s.valueColor, totalCPU, Reset, s.valueColor, totalMemUsed, Reset,
			s.valueColor, totalMemPerc, Reset)
	} else {
		fmt.Printf("Containers Running: %v%v%v\n", s.valueColor, numContainers, Reset)
	}
	rowPos += 1

	for num, ctr := range containers {
		// Initialize ports info
		var portsInfo string
		for _, port := range ctr.Ports {
			portsInfo += fmt.Sprintf("%d/%s ", port.PublicPort, port.Type)
		}
		var mountInfo string
		for i, mnt := range ctr.Mounts {
			mountInfo += fmt.Sprintf(" %v.Source: %v, Destination: %v", i+1, mnt.Source, mnt.Destination)
		}
		// Convert created timestamp to human-readable format

		moveCursor(rowPos+off+1, 0)
		clearLine()

		if len(ctr.ID)+len(ctr.Image)+len(ctr.Names)+10 >= s.screenCols {
			lName := s.screenCols - (len(ctr.ID) + len(ctr.Names) + 10)
			ctr.Image = ctr.Image[0:lName]
		}

		fmt.Printf("%v%v%v. ID: %v%v%v, Name: %v%v%v\n\r",
			s.valueColor, num+1, Reset, s.valueColor, ctr.ID, Reset,
			s.valueColor, ctr.Names[0], Reset)

		off += 1
		moveCursor(rowPos+off+1, 0)
		clearLine()
		s.containerIDs = append(s.containerIDs, ctr.ID)
		if (rowPos + off + 1) >= (s.screenRows - 1) {
			break
		}
	}
	return nil
}

// Kubectl command to get logs
func (s *sysstat) readPodContainerLog(namespace, podName, containerName string) error {
	cmd := exec.Command("kubectl", "logs", podName, "-n", namespace, "-c", containerName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	lines := strings.Split(string(output), "\n")
	fmt.Printf("\rLogs from %v on pod %v in namespace %v:\n", containerName, podName, namespace)
	for _, line := range lines {
		fmt.Printf("\r%v\n", line)
	}
	return nil
}

// Docker command to get logs
func (s *sysstat) readDockerContainerLog(containerIndex int) error {
	containerID := s.containerIDs[containerIndex]
	cmd := exec.Command("docker", "logs", containerID)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	lines := strings.Split(string(output), "\n")
	fmt.Printf("\rLogs from Container ID %v:\n", containerID)
	for _, line := range lines {
		fmt.Printf("\r%v\n", line)
	}
	return nil
}

func (s *sysstat) readPodContainerLogWithAPI(namespace, podName, containerName string) error {
	if err := s.browseK8sLogsWithArrowKeys(namespace, podName, containerName); err != nil {
		fmt.Printf("Error fetching logs: %v\n", err)
		return err
	}
	return nil
}

func (s *sysstat) printPodDescription(namespace, podName string) error {
	fmt.Println(namespace, podName)
	if err := s.browsePodDescriptionWithArrowKeys(namespace, podName); err != nil {
		fmt.Printf("Error fetching logs: %v\n", err)
		return err
	}
	return nil
}

func (s *sysstat) readDockerContainerLogWithAPI(containerID string) error {
	if err := s.browseDockerLogsWithArrowKeys(containerID); err != nil {
		fmt.Printf("Error fetching logs: %v\n", err)
		return err
	}
	return nil
}

func (s *sysstat) printDockerInspectionContents(containerID string) error {
	if err := s.browseDockerInspectionWithArrowKeys(containerID); err != nil {
		fmt.Printf("Error fetching logs: %v\n", err)
		return err
	}
	return nil
}

func (s *sysstat) enterPodContainerShell(namespace, podName, containerName string) error {
	cmd := exec.Command("kubectl", "exec", "-it", podName, "-n", namespace, "-c", containerName, "--", "sh")

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	s.interactiveModeActive = false
	if err != nil {
		return fmt.Errorf("failed to enter container shell: %w", err)
	}

	return nil
}

func (s *sysstat) enterDockerContainerShell(containerID string) error {
	cmd := exec.Command("docker", "exec", "-it", containerID, "/bin/bash")

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	s.interactiveModeActive = false
	if err != nil {
		return fmt.Errorf("failed to enter container shell: %w", err)
	}

	return nil
}

func (s *sysstat) enterPodContainerShellWithAPI(namespace, podName, containerName string) error {
	command := []string{"--", "sh"}

	req := s.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", containerName).
		Param("stdin", "true").
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "true")

	for _, cmd := range command {
		req = req.Param("command", cmd)
	}

	config, err := clientcmd.BuildConfigFromFlags("", s.kubeconfig)
	if err != nil {
		fmt.Printf("Error loading kubeconfig: %v\n", err)
		return err
	}

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		fmt.Printf("Error creating SPDY executor: %v\n", err)
		return err
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Tty:    true,
	})
	if err != nil {
		fmt.Printf("Error in Stream: %v\n", err)
		return err
	}

	return nil
}

func (s *sysstat) fetchLogs(namespace, podName, containerName string) error {
	logOptions := &v1.PodLogOptions{
		Container: containerName,
	}

	req := s.clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	podLogs, err := req.Stream(context.Background())
	if err != nil {
		return fmt.Errorf("error in opening stream: %v", err)
	}
	defer podLogs.Close()

	reader := bufio.NewReader(podLogs)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		fmt.Printf("\r%v", line)
	}

	return nil
}

func (s *sysstat) browseK8sLogsWithArrowKeys(namespace, podName, containerName string) error {
	logOptions := &v1.PodLogOptions{
		Container: containerName,
	}

	req := s.clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	podLogs, err := req.Stream(context.Background())
	if err != nil {
		return fmt.Errorf("error in opening stream: %v", err)
	}
	defer podLogs.Close()

	err = termbox.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize termbox: %v", err)
	}
	defer termbox.Close()

	lines := []string{}
	reader := bufio.NewReader(podLogs)

	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\n"))
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
			printLogs(lines)
			termbox.Flush()
		}
	}()

	x, y := 0, 0
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyArrowUp:
				if y > 0 {
					y--
				}
			case termbox.KeyArrowDown:
				if y < len(lines)-1 {
					y++
				}
			case termbox.KeyArrowLeft:
				if x > 0 {
					x--
				}
			case termbox.KeyArrowRight:
				x++
			case termbox.KeyEsc, termbox.KeyCtrlC:
				return nil
			}
		case termbox.EventResize:
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		case termbox.EventError:
			return fmt.Errorf("termbox error: %v", ev.Err)
		}

		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		printLogs(lines[y:])
		termbox.Flush()
	}

	return nil
}

func (s *sysstat) browsePodDescriptionWithArrowKeys(namespace, podName string) error {
	cmd := exec.Command("kubectl", "describe", "pod", podName, "-n", namespace)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error executing kubectl describe: %v", err)
	}

	err = termbox.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize termbox: %v", err)
	}
	defer termbox.Close()

	lines := strings.Split(string(output), "\n")

	go func() {
		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		printLogs(lines)
		termbox.Flush()
	}()

	x, y := 0, 0
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyArrowUp:
				if y > 0 {
					y--
				}
			case termbox.KeyArrowDown:
				if y < len(lines)-1 {
					y++
				}
			case termbox.KeyArrowLeft:
				if x > 0 {
					x--
				}
			case termbox.KeyArrowRight:
				x++
			case termbox.KeyEsc, termbox.KeyCtrlC:
				return nil
			}
		case termbox.EventResize:
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		case termbox.EventError:
			return fmt.Errorf("termbox error: %v", ev.Err)
		}

		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		printLogs(lines[y:])
		termbox.Flush()
	}

	return nil
}

func printLogs(lines []string) {
	for i, line := range lines {
		for x, ch := range line {
			termbox.SetCell(x, i, ch, termbox.ColorDefault, termbox.ColorDefault)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *sysstat) browseDockerLogsWithArrowKeys(containerID string) error {
	cmd := exec.Command("docker", "logs", "-f", containerID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	lines := []string{}
	reader := bufio.NewReader(stdout)

	err = termbox.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize termbox: %v", err)
	}
	defer termbox.Close()

	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\n"))
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
			printLogs(lines)
			termbox.Flush()
		}
	}()

	x, y := 0, 0
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyArrowUp:
				if y > 0 {
					y--
				}
			case termbox.KeyArrowDown:
				if y < len(lines)-1 {
					y++
				}
			case termbox.KeyArrowLeft:
				if x > 0 {
					x--
				}
			case termbox.KeyArrowRight:
				x++
			case termbox.KeyEsc, termbox.KeyCtrlC:
				return nil
			}
		case termbox.EventResize:
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		case termbox.EventError:
			return fmt.Errorf("termbox error: %v", ev.Err)
		}

		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		printLogs(lines[y:])
		termbox.Flush()
	}

	return nil
}

func (s *sysstat) browseDockerInspectionWithArrowKeys(containerID string) error {
	cmd := exec.Command("docker", "inspect", containerID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	lines := []string{}
	reader := bufio.NewReader(stdout)

	err = termbox.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize termbox: %v", err)
	}
	defer termbox.Close()

	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\n"))
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
			printLogs(lines)
			termbox.Flush()
		}
	}()

	x, y := 0, 0
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyArrowUp:
				if y > 0 {
					y--
				}
			case termbox.KeyArrowDown:
				if y < len(lines)-1 {
					y++
				}
			case termbox.KeyArrowLeft:
				if x > 0 {
					x--
				}
			case termbox.KeyArrowRight:
				x++
			case termbox.KeyEsc, termbox.KeyCtrlC:
				return nil
			}
		case termbox.EventResize:
			termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		case termbox.EventError:
			return fmt.Errorf("termbox error: %v", ev.Err)
		}

		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
		printLogs(lines[y:])
		termbox.Flush()
	}

	return nil
}

func getLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func simulateCPUUsage(n int) []float64 {
	cpuPercent := make([]float64, n)

	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	for i := range cpuPercent {
		cpuPercent[i] = rand.Float64() * 100
	}

	return cpuPercent
}
