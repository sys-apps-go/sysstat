package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"math"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
	"github.com/shirou/gopsutil/process"
	"golang.org/x/term"
	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"
)

const (
	MainScreen        = "main"
	CPUScreen         = "cpu"
	MemoryScreen      = "memory"
	DiskScreen        = "disk"
	ProcessScreen     = "process"
	NetScreen         = "net"
	KubernetesScreen  = "kubernetes"
	DockerScreen      = "docker"
	DockerLogs        = "dockerlogs"
	DockerInteractive = "dockerinteractive"
	DockerInspection  = "dockerinspection"
	K8sLogs           = "k8slogs"
	K8sPodDescription = "k8spoddescription"
	K8sInteractive    = "k8sinteractive"
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
	SocketCount   int32
}

type DockerContainerMemUsage struct {
	Name    string
	Used    string
	Percent string
	CPU     string
}

type DockerContainerStatus struct {
	Name     string
	CPUPerc  string
	MemUsage string
	MemPerc  string
	NetIO    string
	BlockIO  string
	PIDs     string
}

type containerInfo struct {
	containerID        string
	containerIndex     int
	containerNamespace string
	containerPodName   string
	containerName      string
	containerPodIP     string
	containerNodeName  string
}

type sysstat struct {
	osType                string
	scanInterval          int
	currentPage           int
	newScreenOption       int32
	nextPageOption        bool
	newPageOption         int32
	currScreenExited      int32
	isScreenResize        int32
	currScreenName        string
	isRootUser            bool
	maxLength             int
	containersRunning     int
	memUsages             []DockerContainerMemUsage
	initIOCounters        bool
	params                sysParameters
	paramsMu              sync.Mutex
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
	spacekeyPressed       bool
	esckeyPressed         bool
	processList           []ProcessInfo
	valueColor            string
	coreIndexStart        int
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
	socketCount       int
}

type ProcessPlotInfo struct {
	Name        string
	ID          int32
	CPUUsage    float64
	MemoryUsage float64
}

const (
	GB = 1024 * 1024 * 1024
	MB = 1024 * 1024
)

const (
	barWidth  = 30
	barChar   = "█"
	emptyChar = " "
)

const (
	defaultScanInterval = 5
	maxContainerList    = 5
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

	if s.osType == "linux" {
		s.params.uStat, err = disk.Usage("/")
	} else {
		s.params.uStat, err = disk.Usage("C:")
	}
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
	var err error
	var rootDevice string

	s.setScreenFlags("main")
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())

	err = s.readHostInfoParams()
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
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")

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
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
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

func (s *sysstat) netScreen() {
	s.setScreenFlags("net")
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())
	s.displayOption = rand.Intn(2)

	s.currRow = 1

	s.maxLength = 30
	loopCount := 0
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	loopStartRow := s.currRow
	for {
		s.currRow = loopStartRow
		s.printNetInfoDetailed()

		s.printProcessSortedBySocketUsage(&loopCount)

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
		}
	}
}

func (s *sysstat) cpuScreen() {
	s.setScreenFlags(CPUScreen)
	defer s.updateExitFlag()

	rand.Seed(time.Now().UnixNano())
	s.coreIndexStart = 1
	s.displayOption = rand.Intn(2)
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")

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

		s.printProcessInfoSortedByCPUUsage(&loopCount, CPUScreen)

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

	moveCursor(1, 0)
	s.maxLength = 40
	for {
		err := s.printDockerContainerInfoDetailed(1)
		if err != nil {
			fmt.Printf("Failed to docker info: %v\n", err)
			return
		}

		moveCursor(s.screenRows-1, 0)
		fmt.Printf("Type container index and l for instance log, i for interactive or s for inspection, esc to exit, r for main screen, q to quit")
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
	browserMode := flag.Bool("browser", false, "Run in browser mode")
	port := flag.Int("port", 50050, "Port to run in browser mode")
	flag.Parse()

	if *scanInterval <= 0 {
		*scanInterval = defaultScanInterval
	}

	if *browserMode {
		ip := getLocalIP()
		if ip == "" {
			ip = "localhost"
		}
		fmt.Printf("Starting server at http://%v:%v...\n", ip, *port)
		startServerForBrowserUI(*scanInterval, *port)
		return
	}

	s := &sysstat{}
	s.cInfo = make([]containerInfo, 0)
	s.containerIDs = make([]string, 0)
	s.kubeconfig = *kubeconfig
	s.coreIndexStart = 1

	s.osType = getOSType()
	if s.osType == "linux" {
		s.valueColor = White
	} else {
		s.valueColor = Green
	}

	s.screenCols, s.screenRows = getTerminalSize()
	if s.screenRows < 24 {
		fmt.Println("Screen size is less than the minimum 24x80, exiting")
		return
	}
	s.scanInterval = *scanInterval

	s.isRootUser = isRoot()
	s.isDockerInstalled = checkDockerInstalled()
	s.params.prevDiskIOCounter = make(map[int32]uint64)

	savedContent := saveScreen()
	defer restoreScreen(savedContent)

	go s.handleResize()
	clearScreen()
	go s.mainScreen()

	time.Sleep(time.Second * 1)

	// Set the current stdin state to raw mode and restore it on exit
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error entering raw mode: %v\n", err)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	buf := make([]byte, 1)

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
			switchScreen(s.netScreen)
		case 't', 'T':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.dockerScreen)
		case 'g', 'G':
			s.updateAndWaitNewScreenOption()
			switchScreen(s.plotScreen)
			s.updateAndWaitNewScreenOption()
			switchScreen(s.mainScreen)
		case 27: 
			if s.currScreenName == CPUScreen {
				s.esckeyPressed = true
			} 
		case ' ':
			if s.currScreenName == ProcessScreen || s.currScreenName == CPUScreen {
				s.spacekeyPressed = true
			} else if s.currScreenName == KubernetesScreen {
				s.nextPageOption = true
			}
		case 's', 'S':
			if s.currScreenName != KubernetesScreen && s.currScreenName != DockerScreen {
				break
			} else if accumulatedValue > 0 && accumulatedValue <= s.containerCount {
				if s.currScreenName == KubernetesScreen {
					s.updateAndWaitNewScreenOption()
					s.currOption = accumulatedValue
					clearScreen()
					s.k8sPodDescriptionScreen()
					switchScreen(s.k8sScreen)
				} else {
					s.updateAndWaitNewScreenOption()
					s.currOption = accumulatedValue
					clearScreen()
					s.docketContainerInspectionScreen()
					switchScreen(s.dockerScreen)
				}
			}
			accumulatedValue = 0
		case 'l', 'L':
			if s.currScreenName != KubernetesScreen && s.currScreenName != DockerScreen {
				break
			} else if accumulatedValue > 0 && accumulatedValue <= s.containerCount {
				s.currOption = accumulatedValue
				if s.currScreenName == KubernetesScreen {
					s.updateAndWaitNewScreenOption()
					clearScreen()
					s.k8sLogScreen()
					switchScreen(s.k8sScreen)
				} else {
					s.updateAndWaitNewScreenOption()
					clearScreen()
					s.dockerLogScreen()
					switchScreen(s.dockerScreen)
				}
			}
			accumulatedValue = 0
		case 'i', 'I':
			if s.currScreenName != KubernetesScreen && s.currScreenName != DockerScreen {
				break
			} else if accumulatedValue > 0 && accumulatedValue <= s.containerCount {
				s.currOption = accumulatedValue
				if s.currScreenName == KubernetesScreen {
					s.updateAndWaitNewScreenOption()
					s.interactiveModeActive = true
					clearScreen()
					s.k8sInteractiveScreen()
					s.interactiveModeActive = false
					switchScreen(s.k8sScreen)
				} else {
					s.updateAndWaitNewScreenOption()
					s.interactiveModeActive = true
					clearScreen()
					s.dockerInteractiveScreen()
					s.interactiveModeActive = false
					switchScreen(s.dockerScreen)
				}
			}
			accumulatedValue = 0
		default:
			if s.currScreenName != KubernetesScreen && s.currScreenName != DockerScreen {
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
				if accumulatedValue > s.containerCount {
					accumulatedValue = 0
				}
			}
		}
	}
}

func (s *sysstat) processScreen() {
	s.setScreenFlags("process")
	defer s.updateExitFlag()
	s.displayOption = rand.Intn(2)

	var start int
	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type space to scroll up, c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, r for main screen, q to quit")
	loopCount := 0
	for {
		if !s.spacekeyPressed {
			start, _ = s.printProcessInfo(false, 0, &loopCount)
		}

		moveCursor(s.screenRows-1, 0)
		for i := 0; i < s.scanInterval; i++ {
			time.Sleep(time.Second * time.Duration(1))
			if s.checkAndUpdate() {
				return
			}
			if s.spacekeyPressed {
				s.spacekeyPressed = false
				start, _ = s.printProcessInfo(true, start, &loopCount)
			}
		}
		s.spacekeyPressed = false
	}
}

func (s *sysstat) plotScreen() {
	s.setScreenFlags("plot")
	defer s.updateExitFlag()

	moveCursor(s.screenRows-1, 0)
	fmt.Printf("Type c for CPU status, m for memory status, p for process status, k for kubernetes, t for docker instances, g to plot, r for main screen, q to quit")
	s.plotCPUUsage()
}

func (s *sysstat) printProcessInfo(scrollOnly bool, offset int, loopCount *int) (int, error) {
	startOffset := offset
	if !scrollOnly {
		processList, err := s.getProcessList(CPUScreen)
		if err != nil {
			fmt.Printf("Error retrieving processes: %v\n", err)
			return 0, err
		}

		// Sort based on CPU usage
		sort.Slice(processList, func(i, j int) bool {
			return processList[i].CPUUsage > processList[j].CPUUsage
		})

		s.processList = processList
		startOffset = 0
		if *loopCount%60 == 0 {
			s.printContainerStatusBrief(1, len(s.processList), *loopCount)
		}
		*loopCount = *loopCount + 1
	}

	processCountPerPage := s.screenRows - 5

	if startOffset+processCountPerPage >= len(s.processList) {
		startOffset = 0
	}
	paginatedList := s.processList[startOffset : startOffset+processCountPerPage]

	for i := 0; i < processCountPerPage; i++ {
		moveCursor(i+3, 0)
		p := paginatedList[i]
		rss := formatMemoryGB(float64(p.RSS))
		vms := formatMemoryGB(float64(p.VMS))
		path := p.Command
		if len(path) > 80 {
			path = path[0:80]
		} else if len(path) == 0 {
			path = p.Name
		}
		clearLine()
		fmt.Printf("PID: %v%-6d%v CPU: %v%-6.2f%%%v  Memory: %v%-6.2f%%%v  RSS: %v%-8v%v VMS: %v%-8v%v Command: %v%-80s%v\n",
			s.valueColor, p.PID, Reset, s.valueColor, p.CPUUsage, Reset, s.valueColor, p.MemUsage, Reset,
			s.valueColor, rss, Reset, s.valueColor, vms, Reset, s.valueColor, path, Reset)
	}
	startOffset += processCountPerPage
	return startOffset, nil
}

func (s *sysstat) plotCPUUsage() {
	if err := ui.Init(); err != nil {
		fmt.Printf("failed to initialize termui: %v\n", err)
		return
	}
	defer ui.Close()

	itemsToPlot := 1 + 1 + 2 //CPU and Memory Usage + top two cpu consuming processes

	// Initialize data slices for each core
	n := s.screenCols - 5
	data := make([][]float64, itemsToPlot)
	for i := range data {
		data[i] = make([]float64, n)
		for j := range data[i] {
			data[i][j] = 100
		}
	}

	// Calculate dimensions
	plotHeight := s.screenRows * 2 / 3 // Use 2/3 of screen height for the plot

	// Adjust the plot widget
	p0 := widgets.NewPlot()
	p0.Title = "CPU and Memory Usage + Top two Processes by CPU usage"
	p0.Data = data
	p0.SetRect(0, 0, s.screenCols, plotHeight)
	p0.AxesColor = ui.ColorWhite
	p0.ShowAxes = true
	p0.DrawDirection = widgets.DrawLeft
	p0.HorizontalScale = 1

	// Set different colors for each core
	p0.LineColors = getDistinctColors(itemsToPlot)

	// Set labels
	p0.DataLabels = make([]string, itemsToPlot)
	for i := range p0.DataLabels {
		if i == 0 {
			p0.DataLabels[i] = fmt.Sprintf("CPU")
		} else if i == 1 {
			p0.DataLabels[i] = fmt.Sprintf("Memory")
		} else {
			p0.DataLabels[i] = fmt.Sprintf("%v: %v", "Process", i-1)
		}
	}

	// Adjust the process list widget
	processList := widgets.NewList()
	processList.Title = "Top 5 Processes by CPU and Memory Usage"
	processList.SetRect(0, plotHeight+1, s.screenCols, s.screenRows)

	drawTicker := time.NewTicker(time.Second)
	uiEvents := ui.PollEvents()

	for i := 0; ; i++ {
		select {
		case e := <-uiEvents:
			if e.Type == ui.KeyboardEvent {
				ch := e.ID
				if ch == "q" || ch == "<C-c>" || ch == "c" || ch == "m" || ch == "p" || ch == "d" ||
					ch == "k" || ch == "t" || ch == "r" || ch == "<Escape>" {
					return
				}
			}
		case <-drawTicker.C:
			percentCpu, err := cpu.Percent(0, false)
			if err != nil {
				fmt.Printf("Error getting CPU usage: %v\n", err)
				continue
			}
			vMem, err := mem.VirtualMemory()
			if err != nil {
				fmt.Printf("Error getting Memory usage: %v\n", err)
				continue
			}

			copy(data[0], data[0][1:])
			data[0][n-1] = percentCpu[0]

			copy(data[1], data[1][1:])
			data[1][n-1] = float64(vMem.UsedPercent)

			processes, _ := process.Processes()
			processInfos := make([]ProcessPlotInfo, 0)

			for _, p := range processes {
				cpu, _ := p.CPUPercent()
				mem, _ := p.MemoryPercent()
				name, _ := p.Name()
				processInfos = append(processInfos, ProcessPlotInfo{
					Name:        name,
					ID:          p.Pid,
					CPUUsage:    cpu,
					MemoryUsage: float64(mem),
				})
			}

			sort.Slice(processInfos, func(i, j int) bool {
				return processInfos[i].CPUUsage > processInfos[j].CPUUsage
			})

			copy(data[2], data[2][1:])
			data[2][n-1] = float64(processInfos[0].CPUUsage)

			copy(data[3], data[3][1:])
			data[3][n-1] = float64(processInfos[1].CPUUsage)

			p0.Data = data

			processListItems := make([]string, 0)
			processListItems = append(processListItems, fmt.Sprintf("Total CPU and Memory Usage: CPU: %.2f%%, Memory: %.2f%%",
				percentCpu[0], vMem.UsedPercent))
			for i := 0; i < 5 && i < len(processInfos); i++ {
				processListItems = append(processListItems, fmt.Sprintf("%v (PID: %v) - CPU: %.2f%%, Memory: %.2f%%",
					processInfos[i].Name, processInfos[i].ID, processInfos[i].CPUUsage, processInfos[i].MemoryUsage))
			}
			processList.Rows = processListItems

			ui.Render(p0, processList)
		}
	}
}

func getDistinctColors(numColors int) []ui.Color {
	baseColors := []ui.Color{
		ui.ColorRed,
		ui.ColorGreen,
		ui.ColorYellow,
		ui.ColorBlue,
		ui.ColorMagenta,
		ui.ColorCyan,
		ui.ColorWhite,
	}

	// Create variations of base colors
	colors := make([]ui.Color, numColors)
	for i := 0; i < numColors; i++ {
		baseColor := baseColors[i%len(baseColors)]
		variation := uint8(16 * (i / len(baseColors)))
		colors[i] = ui.Color(uint8(baseColor) + variation)
	}

	return colors
}

func getOSType() string {
	osType := "linux"
	switch runtime.GOOS {
	case "windows":
		osType = "windows"
	case "linux":
	case "darwin":
	default:
	}
	return osType
}

func getDockerContainerCPUUsageSum() (int, float64, error) {
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{.Container}}|{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}|{{.BlockIO}}|{{.PIDs}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		fmt.Println("Error executing command:", err)
		return 0, 0, err
	}

	output := out.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	containerCount := 0
	var totalCPUUsage float64
	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) < 8 {
			continue
		}

		cpuPerc, err := strconv.ParseFloat(strings.TrimSuffix(fields[2], "%"), 64)
		if err != nil {
			fmt.Printf("Error parsing CPU percentage for container %s: %v\n", fields[1], err)
			cpuPerc = 0
		}

		containerCount++
		totalCPUUsage += cpuPerc
	}

	return containerCount, totalCPUUsage, nil
}

func getTotalContainerUsage() (int, float64, float64, float64, error) {
	cmd := exec.Command("sudo", "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("error executing docker stats command: %v", err)
	}

	output := out.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	var totalCPU float64
	var totalMemUsed float64
	var totalMemPerc float64

	containerCount := 0
	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) != 3 {
			continue
		}

		// Process CPU percentage
		cpuStr := strings.TrimSuffix(fields[0], "%")
		cpu, err := strconv.ParseFloat(cpuStr, 64)
		if err == nil {
			totalCPU += cpu
		}

		// Process memory usage
		memParts := strings.Fields(fields[1])
		if len(memParts) > 0 {
			memValueStr := strings.TrimSuffix(memParts[0], "MiB")
			memValue, err := strconv.ParseFloat(memValueStr, 64)
			if err == nil {
				totalMemUsed += memValue
			}
		}

		// Process memory percentage
		memPercStr := strings.TrimSuffix(fields[2], "%")
		memPerc, err := strconv.ParseFloat(memPercStr, 64)
		if err == nil {
			totalMemPerc += memPerc
		}
		containerCount++
	}

	return containerCount, totalCPU, totalMemUsed, totalMemPerc, nil
}

func drawBar(percentage float64) string {
	filled := int(math.Round(percentage / 100 * barWidth))
	
	var colorFunc func(a ...interface{}) string
	if percentage > 90 {
		colorFunc = color.New(color.FgRed).SprintFunc()
	} else if percentage > 70 {
		colorFunc = color.New(color.FgYellow).SprintFunc()
	} else {
		colorFunc = color.New(color.FgBlue).SprintFunc()
	}

	bar := fmt.Sprintf("[%s%s] %.2f%%",
		colorFunc(strings.Repeat(barChar, filled)),
		strings.Repeat(emptyChar, barWidth-filled),
		percentage)
	return bar
}

func isDocker() bool {
	// Check if the process is running inside Docker by reading /proc/self/cgroup
	file, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return false
	}
	defer file.Close()

	// Read the file and look for 'docker' in its contents
	var buffer [512]byte
	n, err := file.Read(buffer[:])
	if err != nil && err.Error() != "EOF" {
		return false
	}

	return strings.Contains(string(buffer[:n]), "docker")
}
