package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/nsf/termbox-go"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
	"github.com/shirou/gopsutil/process"
	"github.com/shirou/gopsutil/v3/docker"
	"golang.org/x/term"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	MainScreen       = "main"
	CPUScreen        = "main"
	MemoryScreen     = "memory"
	DiskScreen       = "disk"
	ProcessScreen    = "process"
	KubernetesScreen = "kubernetes"
	DockerScreen     = "docker"
)

type ProcessInfo struct {
	PID           int32
	Name          string
	Command       string
	CPUUsage      float64
	RSS           uint64
	VMS           uint64
	MemUsage      float64
	RdWrBytesPrev uint64
	RdWrBytes     uint64
}

type ContainerMemUsage struct {
	Name    string
	Used    string
	Percent string
}

type containerInfo struct {
	containerID        int
	containerNamespace string
	containerPodName   string
	containerName      string
	containerPodIP     string
	containerNodeName  string
}

type sysstat struct {
	scanInterval          int
	newScreenOption       int32
	nextPageOption        bool
	newPageOption         int32
	currScreenExited      int32
	isScreenResize        int32
	currScreenName        string
	isRootUser            bool
	maxLength             int
	containersRunning     int
	memUsages             []ContainerMemUsage
	initIOCounters        bool
	params                sysParameters
	displayOption         int
	printCPUCores         int
	isDockerInstalled     bool
	screenRows            int
	screenCols            int
	currRow               int
	cInfo                 []containerInfo
	currOption            int
	interactiveModeActive bool
	containerCount        int
	kubeconfig            string
	clientset             *kubernetes.Clientset
	containerIDs          []string
}

type sysParameters struct {
	cpuCores          int
	hostInfo          *host.InfoStat
	upTime            time.Duration
	bootTime          time.Time
	cpuInfo           []cpu.InfoStat
	cpuPercent        []float64
	cpuTimes          []cpu.TimesStat
	vMem              *mem.VirtualMemoryStat
	sMem              *mem.SwapMemoryStat
	pStat             []disk.PartitionStat
	uStat             *disk.UsageStat
	ioCounters        map[string]disk.IOCountersStat
	iStat             []net.InterfaceStat
	netIOCounters     []net.IOCountersStat
	avgStat           *load.AvgStat
	prevReadCount     uint64
	prevWriteCount    uint64
	prevReadBytes     uint64
	prevWriteBytes    uint64
	vMemTotal         uint64
	prevDiskIOCounter map[int32]uint64
	prevContainers    int
}

const (
	GB = 1024 * 1024 * 1024
	MB = 1024 * 1024
)

const (
	defaultScanInterval = 5
	maxContainerList    = 15
)

const (
	Reset  = "\033[0m"
	White  = "\033[37m"
	Yellow = "\033[33m"
	Red    = "\033[31m"
	Blue   = "\033[94m"
	Green  = "\033[38;2;0;255;0m"
)

var err error

func (s *sysstat) readHostInfoParams() error {
	s.params.hostInfo, err = host.Info()
	if err != nil {
		return err
	}
	s.params.upTime = time.Duration(s.params.hostInfo.Uptime) * time.Second
	s.params.bootTime = time.Unix(int64(s.params.hostInfo.BootTime), 0)
	return nil
}

func (s *sysstat) readCPUParams(perCPU bool) error {
	s.params.cpuInfo, err = cpu.Info()
	if err != nil {
		return err
	}
	s.params.cpuPercent, err = cpu.Percent(time.Second, perCPU)
	if err != nil {
		return err
	}
	return nil
}

func (s *sysstat) readCPUTimesParams() error {
	s.params.cpuTimes, err = cpu.Times(false)
	if err != nil {
		return err
	}
	return nil
}

func (s *sysstat) readMemParams() error {
	s.params.vMem, err = mem.VirtualMemory()
	if err != nil {
		return err
	}
	s.params.vMemTotal = s.params.vMem.Total

	s.params.sMem, err = mem.SwapMemory()
	if err != nil {
		return err
	}
	return nil
}

func (s *sysstat) readDiskParams(rootDevice string) error {
	s.params.pStat, err = disk.Partitions(true)
	if err != nil {
		return err
	}
	s.params.uStat, err = disk.Usage("/")
	if err != nil {
		return err
	}
	s.params.ioCounters, err = disk.IOCounters(rootDevice)
	if err != nil {
		return err
	}
	if !s.initIOCounters {
		for _, counters := range s.params.ioCounters {
			s.params.prevReadCount = counters.ReadCount
			s.params.prevWriteCount = counters.WriteCount
			s.params.prevReadBytes = counters.ReadBytes
			s.params.prevWriteBytes = counters.WriteBytes
			break
		}
		s.initIOCounters = true
	}
	return nil
}

func (s *sysstat) readNetParams() error {
	s.params.iStat, err = net.Interfaces()
	if err != nil {
		return err
	}
	s.params.netIOCounters, err = net.IOCounters(true)
	if err != nil {
		return err
	}
	return nil
}

func (s *sysstat) readLoadParams() error {
	s.params.avgStat, err = load.Avg()
	if err != nil {
		return err
	}
	return nil
}

func (s *sysstat) setScreenFlags(name string) {
	atomic.StoreInt32(&s.newScreenOption, 0)
	atomic.StoreInt32(&s.newPageOption, 0)
	atomic.StoreInt32(&s.currScreenExited, 0)
	s.currScreenName = name
}

func (s *sysstat) checkAndUpdate() bool {
	newScreenOption := atomic.LoadInt32(&s.newScreenOption)
	isScreenResize := atomic.LoadInt32(&s.isScreenResize)

	if newScreenOption == 1 || isScreenResize == 1 {
		atomic.StoreInt32(&s.currScreenExited, 1)
		return true
	}

	return false
}

func (s *sysstat) updateExitFlag() {
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) mainScreen() {
	s.setScreenFlags("main")
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())

	err := s.readHostInfoParams()
	if err != nil {
		fmt.Printf("Failed to get host info: %v\n", err)
		return
	}

	err = s.readCPUParams(true)
	if err != nil {
		fmt.Printf("Failed to get CPU info: %v\n", err)
		return
	}

	count, err := cpu.Counts(true) // `true` for logical CPUs
	if err != nil {
		fmt.Printf("Failed to get CPU count: %v\n", err)
		return
	}
	s.params.cpuCores = count

	err = s.readMemParams()
	if err != nil {
		fmt.Printf("Failed to get memory info: %v\n", err)
		return
	}

	// Find the mount point for the root filesystem ("/")
	rootDevice, err := findDeviceForMount("/")
	if err != nil {
		fmt.Printf("Failed to find device for root filesystem: %v\n", err)
		return
	}

	err = s.readDiskParams(rootDevice)
	if err != nil {
		fmt.Printf("Failed to get disk info: %v\n", err)
		return
	}

	err = s.readNetParams()
	if err != nil {
		fmt.Printf("Failed to get net info: %v\n", err)
		return
	}

	err = s.readLoadParams()
	if err != nil {
		fmt.Printf("Failed to get load info: %v\n", err)
		return
	}

	s.maxLength = 30 // maximum length of progress indicator(bar or arrow)
	s.displayOption = rand.Intn(2)
	s.printCPUCores = rand.Intn(2)

	s.currRow = 1

	s.printHostInfo()

	s.printCPUInfo()

	s.printMemoryInfo()

	s.printDiskInfo()

	s.printNetInfo()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")

	loopCount := 0
	loopStartRow := s.currRow
	for {
		s.currRow = loopStartRow
		s.printUpdatedCPUInfo()
		s.currRow += 1

		s.printUpdatedMemInfo()

		s.currRow += 1
		s.printUpdatedDiskInfo()
		s.currRow += 1

		s.printUpdatedNetInfo()

		s.currRow += 2
		s.printProcessInfoSortedByCPUUsage(&loopCount, "main")

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
		}

		err = s.readCPUParams(true)
		if err != nil {
			fmt.Printf("Failed to get CPU info: %v\n", err)
			return
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

		err = s.readNetParams()
		if err != nil {
			fmt.Printf("Failed to get net info: %v\n", err)
			return
		}

		err = s.readLoadParams()
		if err != nil {
			fmt.Printf("Failed to get load info: %v\n", err)
			return
		}

	}
}

func (s *sysstat) memoryScreen() {
	s.setScreenFlags("memory")
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())
	s.displayOption = rand.Intn(2)

	err = s.readMemParams()
	if err != nil {
		fmt.Printf("Failed to get memory info: %v\n", err)
		return
	}

	s.currRow = 1
	s.printMemoryInfo()

	s.maxLength = 30
	loopCount := 0
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
	loopStartRow := s.currRow
	for {
		s.currRow = loopStartRow
		s.printUpdatedMemInfo()

		s.printProcessSortedByMemoryUsage(&loopCount)

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
	}
}

func (s *sysstat) cpuScreen() {
	s.setScreenFlags("cpu")
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())

	s.displayOption = rand.Intn(2)
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")

	moveCursor(1, 0)
	err := s.readHostInfoParams()
	if err != nil {
		fmt.Printf("Failed to get host info: %v\n", err)
		return
	}

	err = s.readCPUParams(true)
	if err != nil {
		fmt.Printf("Failed to get CPU info: %v\n", err)
		return
	}

	err = s.readLoadParams()
	if err != nil {
		fmt.Printf("Failed to get load info: %v\n", err)
		return
	}

	s.currRow = 1
	s.printCPUInfo()

	s.maxLength = 40
	loopCount := 0
	loopStartRow := s.currRow
	for {
		s.currRow = loopStartRow
		err := s.readCPUTimesParams()
		if err != nil {
			fmt.Printf("Failed to read CPU Times info: %v\n", err)
			return
		}

		s.printUpdatedCPUTimesInfo()

		s.printProcessInfoSortedByCPUUsage(&loopCount, "cpu")

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
		}

		err = s.readCPUParams(true)
		if err != nil {
			fmt.Printf("Failed to get CPU info: %v\n", err)
			return
		}

		err = s.readLoadParams()
		if err != nil {
			fmt.Printf("Failed to get load info: %v\n", err)
			return
		}
	}
}

func (s *sysstat) dockerScreen() {
	s.setScreenFlags(DockerScreen)
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")

	moveCursor(1, 0)
	s.maxLength = 40
	for {
		err := s.printDockerContainerInfoDetailed(1)
		if err != nil {
			fmt.Printf("Failed to docker info: %v\n", err)
			return
		}

		moveCursor(s.screenRows-1, 0)
		fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
		for i := 0; i < 60; i++ {
			time.Sleep(time.Second)
			if s.checkAndUpdate() {
				return
			}
		}
	}
}

// Function to handle switching screens
func switchScreen(screenFunc func()) {
	clearScreen()
	go screenFunc()
}

func main() {
	scanInterval := flag.Int("i", defaultScanInterval, "System status display interval")
	kubeconfig := flag.String("k", os.Getenv("KUBECONFIG"), "Kubernetes enviornment")
	flag.Parse()

	if *scanInterval <= 0 {
		*scanInterval = defaultScanInterval
	}

	s := &sysstat{}
	s.cInfo = make([]containerInfo, 0)
	s.containerIDs = make([]string, 0)
	s.kubeconfig = *kubeconfig

	s.screenCols, s.screenRows = getTerminalSize()
	if s.screenRows < 24 {
		fmt.Println("Screen size is less than the minimum 24x80, exiting")
		return
	}
	s.scanInterval = *scanInterval

	s.isRootUser = isRoot()
	s.isDockerInstalled = checkDockerInstalled()
	s.params.prevDiskIOCounter = make(map[int32]uint64)

	saveScreen()
	defer restoreScreen()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGWINCH)

	// Run resize handler in a separate goroutine.
	go s.handleResize(sigc)

	clearScreen()
	go s.mainScreen()

	time.Sleep(time.Second * 1)

	// Get the current terminal state so we can restore it later
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error entering raw mode: %v\n", err)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	buf := make([]byte, 1)

	enterInteractiveMode := false
	printLogInfo := false
	s.interactiveModeActive = false
	accumulatedValue := 0
	for {
		for s.interactiveModeActive {
			time.Sleep(time.Second)
		}
		_, err := os.Stdin.Read(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			break
		}
		switch buf[0] {
		case 'q', 'Q', 3:
			clearScreen()
			return
		case 'm', 'M':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.memoryScreen)
		case 'r', 'R':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.mainScreen)
		case 'p', 'P':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.processScreen)
		case 'c', 'C':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.cpuScreen)
		case 'd', 'D':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.diskScreen)
		case 'k', 'K':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.k8sScreen)
		case 'n', 'N':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.dockerScreen)
		case 'l', 'L':
			printLogInfo = true
			fallthrough
		case 'i', 'I':
			enterInteractiveMode = true
			fallthrough
		default:
			if s.currScreenName != KubernetesScreen {
				break
			}
			input := buf[0]

			if input == ' ' {
				s.nextPageOption = true
				break
			}

			if input >= '0' && input <= '9' {
				digit := int(input - '0')
				accumulatedValue = accumulatedValue*10 + digit
			}
			if !printLogInfo && !enterInteractiveMode {
				break
			}
			if accumulatedValue > s.containerCount {
				accumulatedValue = 0
				printLogInfo = false
				enterInteractiveMode = false
				break
			}
			if accumulatedValue > 0 && accumulatedValue <= s.containerCount {
				s.currOption = accumulatedValue
				s.updateAndWaitNewScreenOption()
				if printLogInfo {
					printLogInfo = false
					clearScreen()
					s.k8sLogScreen()
					switchScreen(s.k8sScreen)
				} else if enterInteractiveMode {
					enterInteractiveMode = false
					s.interactiveModeActive = true
					clearScreen()
					s.k8sInteractiveScreen()
					switchScreen(s.k8sScreen)
				}
				accumulatedValue = 0
			}
			printLogInfo = false
			enterInteractiveMode = false
		}
	}
}

func (s *sysstat) processScreen() {
	s.setScreenFlags("process")
	defer s.updateExitFlag()
	s.displayOption = rand.Intn(2)

	loopCount := 0
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
	for {
		s.printProcessInfoSortedByCPUUsage(&loopCount, "process")

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
		}
	}
}

func (s *sysstat) getProcessListSorted(sortType string) ([]ProcessInfo, error) {
	var processList []ProcessInfo

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

		if sortType == "disk" {
			ioCounters, err := p.IOCounters()
			if err != nil {
				continue
			}
			diskIO = uint64(ioCounters.ReadBytes+ioCounters.WriteBytes) / (1024 * 1024)
		}

		processInfo := ProcessInfo{
			PID:      p.Pid,
			Name:     name,
			Command:  cmd,
			CPUUsage: cpuUsage,
			MemUsage: memUsage,
			RSS:      memInfo.RSS / (1024 * 1024),
			VMS:      memInfo.VMS / (1024 * 1024),
		}

		if sortType == "disk" {
			_, exists := s.params.prevDiskIOCounter[p.Pid]
			if !exists {
				s.params.prevDiskIOCounter[p.Pid] = diskIO
			}
			processInfo.RdWrBytes = diskIO - s.params.prevDiskIOCounter[p.Pid]
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
			fmt.Printf("Total Processes: %v%v%v, Containers Running: %v%v%v", White, processCount, Reset, White, "None", Reset)
		} else {
			fmt.Printf("Total Processes: %v%v%v, Containers Running: %v%v%v: ", White, processCount, Reset,
				White, containersRunning, Reset)
		}
		count := 1
		if s.currScreenName != "main" && s.currScreenName != "process" && s.currScreenName != "cpu" {
			for _, stat := range dockerStats {
				if !strings.Contains(stat.Status, "Exited") && stat.Running {
					if count == containersRunning {
						fmt.Printf("%v%v%v.Image: %v%v%v, Port: %v%v%v", White, count, Reset, White, stat.Image, Reset,
							White, containerPortMap[stat.Image], Reset)
					} else {
						fmt.Printf("%v%v%v.Image: %v%v%v, Port: %v%v%v, ", White, count, Reset, White, stat.Image, Reset,
							White, containerPortMap[stat.Image], Reset)
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
		fmt.Printf("Total Processes: %v%v%v, Containers Running: ", White, processCount, Reset)
	}
}

func (s *sysstat) printProcessInfoSortedByCPUUsage(loopCount *int, scrName string) {
	var rowPos int
	if scrName == "process" {
		rowPos = 1
	} else {
		rowPos = s.currRow + 1
	}

	processCount := 10

	// Get a list of all processes
	processList, err := s.getProcessListSorted("cpu")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}

	// Sort based on CPU usage
	sort.Slice(processList, func(i, j int) bool {
		return processList[i].CPUUsage > processList[j].CPUUsage
	})

	connections, err := net.Connections("all")
	if err != nil {
		return
	}

	if *loopCount%60 == 0 {
		s.printContainerStatusBrief(rowPos, len(processList), *loopCount)
	}

	*loopCount = *loopCount + 1

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

	if scrName == "process" {
		processToPrint = s.screenRows - 7
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
			fmt.Printf("PID: %v%-5d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Port: %v%-5v%v Command: %v%-60s%v\n",
				White, p.PID, Reset, White, p.CPUUsage, Reset, White, p.MemUsage, Reset,
				White, rss, Reset, White, vms, Reset, White, port, Reset, White, path, Reset)
		} else {
			fmt.Printf("PID: %v%-5d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
				White, p.PID, Reset, White, p.CPUUsage, Reset, White, p.MemUsage, Reset,
				White, rss, Reset, White, vms, Reset, White, path, Reset)
		}
	}
	moveCursor(s.screenRows-2, 0)
	clearLine()

}

func (s *sysstat) printProcessSortedByMemoryUsage(loopCount *int) {
	rowPos := s.currRow + 1
	processCount := 10
	*loopCount = *loopCount + 1

	// Get a list of all processes
	processList, err := s.getProcessListSorted("memory")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}
	// Sort based on CPU usage
	sort.Slice(processList, func(i, j int) bool {
		return processList[i].MemUsage > processList[j].MemUsage
	})

	connections, err := net.Connections("all")
	if err != nil {
		return
	}

	s.printDockerContainerInfo(rowPos, *loopCount, len(processList))

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
			fmt.Printf("PID: %v%-5d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Port: %v%-5v%v Command: %v%-60s%v\n",
				White, p.PID, Reset, White, p.CPUUsage, Reset, White, p.MemUsage, Reset,
				White, rss, Reset, White, vms, Reset, White, port, Reset, White, path, Reset)
		} else {
			fmt.Printf("PID: %v%-5d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
				White, p.PID, Reset, White, p.CPUUsage, Reset, White, p.MemUsage, Reset,
				White, rss, Reset, White, vms, Reset, White, path, Reset)
		}
	}

}

func (s *sysstat) diskScreen() {
	rand.Seed(time.Now().UnixNano())
	s.setScreenFlags("disk")
	s.displayOption = rand.Intn(2)
	defer s.updateExitFlag()

	err := s.readHostInfoParams()
	if err != nil {
		fmt.Printf("Failed to get host info: %v\n", err)
		return
	}

	// Find the mount point for the root filesystem ("/")
	rootDevice, err := findDeviceForMount("/")
	if err != nil {
		fmt.Printf("Failed to find device for root filesystem: %v\n", err)
		return
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
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
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

func saveScreen() {
	// Save the current screen state
	fmt.Print("\x1b[s")
}

func restoreScreen() {
	// Restore the saved screen state
	fmt.Print("\x1b[u")
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

func (s *sysstat) handleResize(c <-chan os.Signal) {
	for sig := range c {
		if sig == syscall.SIGWINCH {
			s.updateAndWaitResizeEvent()
			s.screenCols, s.screenRows = getTerminalSize()
			if s.screenRows < 24 {
				fmt.Println("Screen size is less than the minimum 24x80, exiting")
				return
			}
			switch s.currScreenName {
			case "main":
				switchScreen(s.mainScreen)
			case "cpu":
				switchScreen(s.cpuScreen)
			case "process":
				switchScreen(s.processScreen)
			case "memory":
				switchScreen(s.memoryScreen)
			case "disk":
				switchScreen(s.diskScreen)
			case "kubernetes":
				switchScreen(s.k8sScreen)
			case "docker":
				switchScreen(s.dockerScreen)
			case "kuberneteslogs":
				switchScreen(s.k8sLogScreen)
			case "kubernetesinteractive":
				switchScreen(s.k8sInteractiveScreen)
			}
		}
	}
}

func getTerminalSize() (width, height int) {
	if col, row, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return col, row
	}
	return 80, 24 // Default size if GetSize fails
}

func getContainerMemoryUsage() []ContainerMemUsage {
	var memUsages []ContainerMemUsage
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

	// Define regex pattern to extract memory usage and limit
	memUsagePatternGb := `(\d+\.\d+)GiB / (\d+\.\d+)GiB`
	memUsagePatternMb := `(\d+\.\d+)MiB / (\d+\.\d+)GiB`
	memUsageRegexGb := regexp.MustCompile(memUsagePatternGb)
	memUsageRegexMb := regexp.MustCompile(memUsagePatternMb)

	for _, line := range lines {
		foundMb := false
		if line == "" {
			continue
		}

		// Find the memory usage and limit
		matches := memUsageRegexGb.FindStringSubmatch(line)
		if len(matches) == 0 {
			matches = memUsageRegexMb.FindStringSubmatch(line)
			if len(matches) == 0 {
				continue
			}
			foundMb = true
		}

		memUsageStr := matches[1]
		memLimitStr := matches[2]

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

		m := ContainerMemUsage{}
		fl := strings.Split(line, " ")
		m.Name = fl[3]
		// Calculate memory usage percentage
		if foundMb {
			memUsagePercentage := ((memUsage * 100) / (memLimit * 1024))
			m.Percent = fmt.Sprintf("%.2f%%", memUsagePercentage)
			m.Used = fmt.Sprintf("%v MiB", memUsage)
		} else {
			memUsagePercentage := (memUsage / memLimit) * 100
			m.Percent = fmt.Sprintf("%.2f%%", memUsagePercentage)
			m.Used = fmt.Sprintf("%v GiB", memUsage)
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
			fmt.Printf("Total Processes: %v%v%v", White, processCount, Reset)
			moveCursor(containerRow+1, 0)
			fmt.Printf("Containers Running: %v%v%v", White, "None", Reset)
		} else {
			moveCursor(containerRow, 0)
			fmt.Printf("Total Processes: %v%v%v", White, processCount, Reset)
			moveCursor(containerRow+1, 0)
			fmt.Printf("Containers Running: %v%v%v:", White, containersRunning, Reset)
		}

		if s.currScreenName == "memory" {
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
								fmt.Printf("%v.Image: %v%v%v, Memory used: %v%v%v, Percentage: %v%v%v, Port: %v%v%v\n",
									count, White, stat.Image, Reset, White, m.Used, Reset,
									White, m.Percent, Reset, White, containerPortMap[stat.Image], Reset)
								containerRow++
								break
							}
						}
					} else {
						moveCursor(containerRow, 0)
						clearLine()
						fmt.Printf("%v. Image: %v%v%v, Port: %v%v%v\n", count, White, stat.Image, Reset,
							White, containerPortMap[stat.Image], Reset)
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
		fmt.Printf("Total Processes: %v%v%v", White, processCount, Reset)
		moveCursor(containerRow+1, 0)
		fmt.Printf("Containers Running: ")
	}

}

func (s *sysstat) printSortedDiskUsage(loopCount *int) {
	rowPos := s.currRow + 1
	*loopCount = *loopCount + 1

	// Get a list of all processes
	processList, err := s.getProcessListSorted("disk")
	if err != nil {
		fmt.Printf("Error retrieving processes: %v\n", err)
		return
	}

	// Sort based on CPU usage
	sort.Slice(processList, func(i, j int) bool {
		return processList[i].RdWrBytes > processList[j].RdWrBytes
	})

	s.printDockerContainerInfo(rowPos, *loopCount, len(processList))

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

		fmt.Printf("PID: %v%-5d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RdWrSizeMB: %v%-8v%v RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
			White, p.PID, Reset, White, p.CPUUsage, Reset, White, p.MemUsage, Reset,
			White, p.RdWrBytes, Reset, White, rss, Reset, White, vms, Reset, White, path, Reset)
	}

}

// printHostInfo prints the host information
// Print in 1 row
func (s *sysstat) printHostInfo() {
	moveCursor(s.currRow, 0)
	str := fmt.Sprintf("Host: %v%s%v, Platform: %v%s%v %v%s%v, %v%s%v, OS: %v%s%v, Uptime: %v%s%v, Boot Time: %v%s%v, Virtualization: %v%s%v, Role: %v%s%v\n",
		White, s.params.hostInfo.Hostname, Reset,
		White, s.params.hostInfo.Platform, Reset,
		White, s.params.hostInfo.PlatformVersion, Reset,
		White, s.params.hostInfo.KernelVersion, Reset,
		White, s.params.hostInfo.OS, Reset,
		White, formatTime(s.params.upTime), Reset,
		White, s.params.bootTime.Format("Mon, 02 Jan 2006 15:04:05 MST"), Reset,
		White, s.params.hostInfo.VirtualizationSystem, Reset,
		White, s.params.hostInfo.VirtualizationRole, Reset)

	// Calculate the length of the string excluding color codes
	colorCodeLength := len(White) + len(Reset)
	colorCodeCount := 9 // number of color codes in the formatted string
	actualLength := len(str) - (colorCodeLength * colorCodeCount)
	if actualLength < s.screenCols {
		fmt.Printf(str)
	} else {
		fmt.Printf(fmt.Sprintf("%v%v", str[:s.screenCols], Reset))
	}
	s.currRow += 1
}

// Print in 1 row
func (s *sysstat) printCPUInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("CPU Cores: %v%v%v, Specification: %v%s @ %.2fGHz - %.2f/%.2fGHz%v, Cache Size: %v%vK%v\n", White, s.params.cpuCores, Reset,
		White, s.params.cpuInfo[0].ModelName, s.params.cpuInfo[0].Mhz/1000, 0.00, s.params.cpuInfo[0].Mhz/1000, Reset,
		White, s.params.cpuInfo[0].CacheSize, Reset)
	s.currRow += 1
}

// Print in 1 row
func (s *sysstat) printMemoryInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Memory Total: %s%s%s, Buffers: %v%v%v, Cached: %v%v%v\n",
		White, formatMemory(s.params.vMem.Total), Reset,
		White, formatMemory(s.params.vMem.Buffers), Reset,
		White, formatMemory(s.params.vMem.Cached), Reset)
	s.currRow += 1
}

// Print in 2 rows
func (s *sysstat) printDiskInfo() {
	moveCursor(s.currRow, 0)
	fmt.Printf("Disk Total: ")
	count := 0
	var str string
	for _, diskInfo := range s.params.pStat {
		usage, err := disk.Usage(diskInfo.Mountpoint)
		if err == nil {
			if diskInfo.Mountpoint == "/" {
				str = fmt.Sprintf("(%s): %s%s%s ", diskInfo.Mountpoint, White, formatMemory(usage.Total), Reset)
				count++
			} else if !strings.Contains(diskInfo.Mountpoint, "snap") && !strings.HasPrefix(diskInfo.Mountpoint, "/sys") &&
				!strings.HasPrefix(diskInfo.Mountpoint, "/proc") && !strings.HasPrefix(diskInfo.Mountpoint, "/dev") &&
				!strings.HasPrefix(diskInfo.Mountpoint, "/run") && !strings.Contains(diskInfo.Mountpoint, "docker") &&
				count <= 10 {
				str += fmt.Sprintf("(%s): %s%s%s ", diskInfo.Mountpoint, White, formatMemory(usage.Total), Reset)
				count++
			}
		}
	}
	n := len(str) - (count * (len(White) + len(Reset)))
	if n < s.screenCols {
		fmt.Printf(str)
	} else {
		fmt.Printf(fmt.Sprintf("%v%v", str[:s.screenCols], Reset))
	}
	s.currRow += 1
}

// Print in 3 rows
func (s *sysstat) printNetInfo() {
	moveCursor(s.currRow, 0)
	var ports []string
	var str string
	for _, netInterface := range s.params.iStat {
		for _, addr := range netInterface.Addrs {
			ports = append(ports, fmt.Sprintf("%s (%s)", addr.Addr, netInterface.Name))
		}
	}
	str = fmt.Sprintf("\nNetwork Ports: %s%s%s\n", White, strings.Join(ports, ", "), Reset)
	n := len(str) - (1 * (len(White) + len(Reset)))
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
		fmt.Printf("CPU Status: %v%v%v\n", White, cpuTime.String(), Reset)
		s.currRow++
	}
	moveCursor(s.currRow, 0)
	fmt.Printf("CPU Status: Active: %v%s%v  Idle: %v%s%v Average Load: %v%v%v \n",
		White, formatPercentage(cpuUsage), Reset, White, formatPercentage(100-cpuUsage), Reset,
		White, *s.params.avgStat, Reset)

	s.currRow += 2
	moveCursor(s.currRow, 0)

	var coreLabel string
	cores := len(s.params.cpuPercent)
	for i, pct := range s.params.cpuPercent {
		if i > 31 {
			break
		}
		if s.screenRows <= 30 && i > 15 {
			break
		}
		if i < 9 {
			coreLabel = fmt.Sprintf("%v%v%v%v%v%v:", Green, "CPU Core : ", Reset, Blue, i+1, Reset)
		} else {
			coreLabel = fmt.Sprintf("%v%v%v%v%v%v:", Green, "CPU Core :", Reset, Blue, i+1, Reset)
		}

		if s.displayOption == 0 {
			if cores <= 2 {
				printUsageArrow(coreLabel, pct, s.maxLength)
			} else {
				printUsageBar(coreLabel, pct, s.maxLength)
			}
		} else {
			printUsageBar(coreLabel, pct, s.maxLength)
		}

		if i%2 != 0 {
			s.currRow++
		}

		if i != 0 && (i+1)%8 == 0 {
			s.currRow++
		}

		if i != 0 && i%2 != 0 {
			moveCursor(s.currRow, 0)
		}

		if i%2 == 0 {
			fmt.Printf("    ")
		}

	}
}

func (s *sysstat) printUpdatedCPUInfo() {
	cpuUsage := s.params.cpuPercent[0]

	moveCursor(s.currRow, 0)
	clearLine()
	fmt.Printf("CPU Status: Active: %v%s%v, Idle: %v%s%v, Avg Load: %v%v%v,  ",
		White, formatPercentage(cpuUsage), Reset, White, formatPercentage(100-cpuUsage), Reset,
		White, *s.params.avgStat, Reset)

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
		White, formatMemory(s.params.vMem.Available), Reset, White, formatMemory(s.params.vMem.Free), Reset,
		White, formatMemory(s.params.vMem.Used), Reset, White, s.params.vMem.UsedPercent, Reset,
		White, formatMemory(s.params.vMem.Buffers), Reset,
		White, formatMemory(s.params.vMem.Cached), Reset)

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
		White, formatMemory(s.params.sMem.Total), Reset, White, formatMemory(s.params.sMem.Free), Reset,
		White, formatMemory(s.params.sMem.Used), Reset, White, s.params.sMem.UsedPercent, Reset)

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
		White, formatMemory(s.params.sMem.Total), Reset, White, formatMemory(s.params.sMem.Free), Reset,
		White, formatMemory(s.params.sMem.Used), Reset, White, s.params.sMem.UsedPercent, Reset)

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
		White, formatMemory(s.params.uStat.Free), Reset,
		White, formatMemory(s.params.uStat.Used), Reset, White, s.params.uStat.UsedPercent, Reset,
		White, currReadCount, Reset, White, currWriteCount, Reset,
		White, iopsInProgress, Reset,
		White, formatMemoryGB(float64(readBytes)/float64(1024*1024)), Reset, White, formatMemoryGB(float64(writeBytes)/float64(1024*1024)), Reset)

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
			str += fmt.Sprintf("NET: [%v%s%v]  ", White, netIO.Name, Reset)

			str += fmt.Sprintf("In: %v%v%v  Out: %v%v%v, ",
				White, formatData(netIO.BytesRecv), Reset,
				White, formatData(netIO.BytesSent), Reset)
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
	fmt.Printf("Type pod number and l for log contents or i for interactive mode, space for next page, k for kubernetes refresh, r for main screen, q to quit")
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
	fmt.Printf("Number of Worker Nodes: %v%v%v: ", White, workerNodes, Reset)
	count = 1
	for _, node := range nodes.Items {
		if !strings.Contains(node.Name, "control") {
			if count != workerNodes {
				fmt.Printf("%v.Name: %v%v%v, IP: %v%v%v, ", count, White, node.Name, Reset,
					White, node.Status.Addresses[0].Address, Reset)
			} else {
				fmt.Printf("%v.Name: %v%v%v, IP: %v%v%v\n", count, White, node.Name, Reset,
					White, node.Status.Addresses[0].Address, Reset)
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
	fmt.Printf("Number of Namespaces: %v%v%v: ", White, len(namespaces.Items), Reset)
	for i, ns := range namespaces.Items {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%v%v%v", White, ns.Name, Reset)
	}
	fmt.Println()

	// Get pods
	pods, err := s.clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing pods: %s", err.Error())
		return
	}

	containerCount := 0
	s.cInfo = make([]containerInfo, 0)
	for _, pod := range pods.Items {
		if pod.Namespace != "kube-system" && pod.Namespace != "local-path-storage" {
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
	}

	sort.Slice(s.cInfo, func(i, j int) bool {
		return s.cInfo[i].containerPodName < s.cInfo[j].containerPodName
	})

	i := 0
	for i < len(s.cInfo) {
		c := s.cInfo[i]
		c.containerID = i + 1
		s.cInfo[i] = c
		i++
	}

	s.containerCount = containerCount

	moveCursor(6, 0)
	fmt.Printf("Number of Pods(including system): %v%v%v: User Pods: ", White, len(pods.Items), Reset)
	s.printContainerChart(7, 0, &currIndex)

	// Get services
	services, err := s.clientset.CoreV1().Services("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing services: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-14, 0)
	fmt.Printf("Number of Services(including system): %v%v%v: ", White, len(services.Items), Reset)
	n := len(services.Items)
	for i, svc := range services.Items {
		if i > 0 && i != n-1 {
			fmt.Print(", ")
		}
		if !strings.Contains(svc.Namespace, "kube-system") {
			fmt.Printf("%v%v%v (Namespace: %v%v%v, ClusterIP: %v%v%v", White, svc.Name, Reset,
				White, svc.Namespace, Reset, White, svc.Spec.ClusterIP, Reset)

			// Check for LoadBalancer type and print its status
			if svc.Spec.Type == v1.ServiceTypeLoadBalancer {
				if len(svc.Status.LoadBalancer.Ingress) > 0 {
					// Retrieve either IP or Hostname from LoadBalancer Ingress
					ingress := svc.Status.LoadBalancer.Ingress[0]
					if ingress.IP != "" {
						fmt.Printf(", LoadBalancerIP: %v%v%v", White, ingress.IP, Reset)
					} else if ingress.Hostname != "" {
						fmt.Printf(", LoadBalancerHostname: %v%v%v", White, ingress.Hostname, Reset)
					} else {
						fmt.Printf(", LoadBalancerIP: %vPending%v", White, Reset)
					}
				} else {
					fmt.Printf(", LoadBalancerIP: %vNot Assigned%v", White, Reset)
				}
			}
			// Print service ports
			for _, port := range svc.Spec.Ports {
				fmt.Printf(", Port: %v%v%v/%v%v%v", White, port.Port, Reset,
					White, port.Protocol, Reset)
			}
			fmt.Print(")")
		}
	}
	fmt.Println()

	// Get PersistentVolumeClaims
	pvcs, err := s.clientset.CoreV1().PersistentVolumeClaims("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing PersistentVolumeClaims: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-9, 0)
	fmt.Printf("Number of PersistentVolumeClaims: %v%v%v: ", White, len(pvcs.Items), Reset)
	n = len(pvcs.Items)
	for i, pvc := range pvcs.Items {
		if i > 0 && i != n-1 {
			fmt.Print(", ")
		}
		fmt.Printf("%v%v%v (Namespace: %v%v%v, Status: %v%v%v", White, pvc.Name, Reset,
			White, pvc.Namespace, Reset, White, pvc.Status.Phase, Reset)
		fmt.Print(")")
	}
	fmt.Println()

	// Get StorageClasses
	storageClasses, err := s.clientset.StorageV1().StorageClasses().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error listing StorageClasses: %s", err.Error())
		return
	}
	moveCursor(s.screenRows-5, 0)
	fmt.Printf("Number of StorageClasses: %v%v%v: ", White, len(storageClasses.Items), Reset)
	for _, sc := range storageClasses.Items {
		fmt.Printf("%v%v%v", White, sc.Name, Reset)
	}
	fmt.Println()

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
			White, cInfo.containerID, Reset, White, cInfo.containerPodName, Reset,
			White, cInfo.containerName, Reset, White, cInfo.containerNamespace, Reset,
			White, cInfo.containerPodIP, Reset, White, cInfo.containerNodeName, Reset)
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
	s.setScreenFlags("kuberneteslogs")
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
	moveCursor(1, 0)
	s.readPodContainerLogWithAPI(s.cInfo[s.currOption-1].containerNamespace, s.cInfo[s.currOption-1].containerPodName, s.cInfo[s.currOption-1].containerName)
	atomic.StoreInt32(&s.currScreenExited, 1)
}

func (s *sysstat) dockerLogScreen(containerIdx int) {
	s.setScreenFlags("dockerlogs")
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
	moveCursor(1, 0)
	s.readDockerContainerLog(containerIdx)
	for i := 0; i < s.scanInterval; i++ {
		time.Sleep(time.Second * time.Duration(1))
		if s.checkAndUpdate() {
			atomic.StoreInt32(&s.currScreenExited, 1)
			return
		}
	}
}

func (s *sysstat) k8sInteractiveScreen() {
	s.setScreenFlags("kubernetesinteractive")
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, d for disk status, n for docker instances, k for kubernetes, r for main screen, q to quit")
	moveCursor(1, 0)
	s.enterPodContainerShell(s.cInfo[s.currOption-1].containerNamespace, s.cInfo[s.currOption-1].containerPodName, s.cInfo[s.currOption-1].containerName)
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

	// Create a context
	ctx := context.Background()

	// Fetch disk usage information
	usage, err := cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return err
	}

	numContainers := len(containers)
	if s.params.prevContainers != numContainers {
		clearScreen()
		s.params.prevContainers = numContainers
	}
	off := 0
	moveCursor(rowPos+off, 0)
	clearLine()
	layerInfo := fmt.Sprintf("%.2f GB", float64(usage.LayersSize)/GB)
	fmt.Printf("Docker Container Info: Containers Running: %v%v%v, Layers Size: %v%v%v\n",
		White, numContainers, Reset, White, layerInfo, Reset)
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
		createdTime := time.Unix(ctr.Created, 0).Format(time.RFC3339)

		var rootFsSize string
		for _, container := range usage.Containers {
			if container.State == "running" {
				if container.ID == ctr.ID {
					rootFsSize = fmt.Sprintf("%4.2f MB", float64(container.SizeRootFs)/MB)
				}
			}
		}
		// Print container details
		moveCursor(rowPos+off+1, 0)
		clearLine()
		fmt.Printf("%v%v%v. Container ID: %v%v%v, Image: %v%v%v, Names: %v%v%v, Command: %v%v%v, RootFS Size: %v%v%v, Created: %v%v%v, State: %v%v%v, Status: %v%v%v, Mounts: %v%v%v, PortInfo: %v%v%v\n",
			White, num+1, Reset, White, ctr.ID, Reset, White, ctr.Image, Reset, White, ctr.Names, Reset,
			White, ctr.Command, Reset, White, rootFsSize, Reset, White, createdTime, Reset, White, ctr.State,
			Reset, White, ctr.Status, Reset, White, mountInfo, Reset, White, portsInfo, Reset)

		off += 3
		s.containerIDs = append(s.containerIDs, ctr.ID)
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
	if err := s.browseLogsWithArrowKeys(namespace, podName, containerName); err != nil {
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

// execCommand executes a command in a specific container in a pod
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

// fetchLogs fetches logs from a specific container in a pod
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

func (s *sysstat) browseLogsWithArrowKeys(namespace, podName, containerName string) error {
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

// printLogs prints the log lines in the terminal
func printLogs(lines []string) {
	for i, line := range lines {
		for x, ch := range line {
			termbox.SetCell(x, i, ch, termbox.ColorDefault, termbox.ColorDefault)
		}
	}
}
