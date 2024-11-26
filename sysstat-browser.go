package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
	"github.com/shirou/gopsutil/process"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var processIOMap map[int32]uint64
var processIOMapMu sync.Mutex

var kubernetesInitialized bool
var clientSet *kubernetes.Clientset

type SystemInfo struct {
	StaticInfo           StaticInformation     `json:"staticInfo"`
	CPU                  CPUInfo               `json:"cpu"`
	Mem                  MemInfo               `json:"mem"`
	Swap                 SwapInfo              `json:"swap"`
	Disk                 DiskInfo              `json:"disk"`
	Network              NetworkInfo           `json:"network"`
	Processes            []ProcessInfoBrowser  `json:"processes"`
	K8sContainerInfos    []K8sContainerInfo    `json:"k8scontainerinfos"`
	DockerContainerInfos []DockerContainerInfo `json:"dockercontainerinfos"`
}

type StaticInformation struct {
	HostInfo    string `json:"hostInfo"`
	CPUInfo     string `json:"cpuInfo"`
	MemoryTotal string `json:"memoryTotal"`
	DiskTotal   string `json:"diskTotal"`
}

type CPUInfo struct {
	Active  float64            `json:"active"`
	Idle    float64            `json:"idle"`
	AvgLoad map[string]float64 `json:"avg_load"`
}

type MemInfo struct {
	Available   uint64  `json:"available"`
	Free        uint64  `json:"free"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"usedPercent"`
	Buffers     uint64  `json:"buffers"`
	Cached      uint64  `json:"cached"`
}

type SwapInfo struct {
	Total       uint64  `json:"total"`
	Free        uint64  `json:"free"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"usedPercent"`
}

type DiskInfo struct {
	Total       uint64  `json:"total"`
	Free        uint64  `json:"free"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"usedPercent"`
}

type NetworkInfo struct {
	Interfaces []NetworkInterface `json:"interfaces"`
}

type NetworkInterface struct {
	Name      string `json:"name"`
	InBytes   uint64 `json:"inBytes"`
	OutBytes  uint64 `json:"outBytes"`
	IPAddress string `json:"ipAddress"`
}

type ProcessInfoBrowser struct {
	PID         int32   `json:"pid"`
	CPU         float64 `json:"cpu"`
	Memory      float64 `json:"memory"`
	RSS         uint64  `json:"rss"`
	VMS         uint64  `json:"vms"`
	Command     string  `json:"command"`
	DiskIO      uint64  `json:"diskio"`
	PrevDiskIO  uint64  `json:"prevdiskio"`
	SocketCount int32   `json:"socketcount"`
}

type K8sContainerInfo struct {
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName"`
	NodeName      string `json:"nodeName"`
	PodIP         string `json:"podIP"`
	Services      string `json:"services"`
	Namespace     string `json:"namespace"`
}

type DockerContainerInfo struct {
	Name             string `json:"name"`
	CPUPercentage    string `json:"cpuPercentage"`
	MemoryUsage      string `json:"memoryUsage"`
	MemoryPercentage string `json:"memoryPercentage"`
}

func getTopProcesses(screenType string) ([]ProcessInfoBrowser, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, err
	}

	var processInfos []ProcessInfoBrowser
	for _, p := range processes {
		cmdline, err := p.Cmdline()
		if err != nil || len(cmdline) == 0 {
			continue
		}

		pb := ProcessInfoBrowser{
			PID: p.Pid,
		}

		cpu, err := p.CPUPercent()
		if err == nil {
			pb.CPU = cpu
		} else {
			pb.CPU = 0
		}
		mem, err := p.MemoryPercent()
		if err == nil {
			pb.Memory = float64(mem)
		} else {
			pb.Memory = 0
		}
		memInfo, err := p.MemoryInfo()
		if err == nil {
			pb.RSS = memInfo.RSS
			pb.VMS = memInfo.VMS
		} else {
			pb.RSS = 0
			pb.VMS = 0
		}

		if len(cmdline) > 70 {
			cmdline = cmdline[0:70]
		}
		pb.Command = cmdline

		ioCounters, err := p.IOCounters()
		if err == nil {
			diskIO := uint64(ioCounters.ReadBytes+ioCounters.WriteBytes) / (1024 * 1024)
			if diskIO > 0 {
				processIOMapMu.Lock()
				_, exists := processIOMap[p.Pid]
				if !exists {
					processIOMap[p.Pid] = diskIO
					pb.DiskIO = 0
				} else {
					pb.DiskIO = diskIO - processIOMap[p.Pid]
				}
				processIOMapMu.Unlock()
			} else {
				pb.DiskIO = 0
			}
		} else {
			pb.DiskIO = 0
		}

		connections, err := net.ConnectionsPid("tcp", p.Pid)
		if err == nil {
			pb.SocketCount = int32(len(connections))
		} else {
			pb.SocketCount = 0
		}

		processInfos = append(processInfos, pb)
	}

	switch screenType {
	case "main":
		sort.Slice(processInfos, func(i, j int) bool {
			return processInfos[i].CPU > processInfos[j].CPU
		})
	case "memory":
		sort.Slice(processInfos, func(i, j int) bool {
			return processInfos[i].Memory > processInfos[j].Memory
		})
	case "disk":
		// Sort based on Disk IO or CPU usage
		sort.Slice(processInfos, func(i, j int) bool {
			// First, check if both processes have DiskIO > 0
			if processInfos[i].DiskIO > 0 && processInfos[j].DiskIO > 0 {
				// If both have DiskIO > 0, sort by DiskIO descending
				if processInfos[i].DiskIO != processInfos[j].DiskIO {
					return processInfos[i].DiskIO > processInfos[j].DiskIO
				}
				// If DiskIO is the same, sort by CPUUsage descending
				return processInfos[i].CPU > processInfos[j].CPU
			}
			// If only one has DiskIO > 0, it should come first
			if processInfos[i].DiskIO > 0 {
				return true
			}
			if processInfos[j].DiskIO > 0 {
				return false
			}
			// If both have DiskIO <= 0, sort by CPUUsage descending
			return processInfos[i].CPU > processInfos[j].CPU
		})
	case "net":
		// Sort based on Sockets Opened or CPU usage
		sort.Slice(processInfos, func(i, j int) bool {
			// First, check if both processes have SocketCount > 0
			if processInfos[i].SocketCount > 0 && processInfos[j].SocketCount > 0 {
				// If both have SocketCount > 0, sort by SocketCount descending
				if processInfos[i].SocketCount != processInfos[j].SocketCount {
					return processInfos[i].SocketCount > processInfos[j].SocketCount
				}
				// If SocketCount is the same, sort by CPUUsage descending
				return processInfos[i].CPU > processInfos[j].CPU
			}
			// If only one has SocketCount > 0, it should come first
			if processInfos[i].SocketCount > 0 {
				return true
			}
			if processInfos[j].SocketCount > 0 {
				return false
			}
			// If both have SocketCount <= 0, sort by CPUUsage descending
			return processInfos[i].CPU > processInfos[j].CPU
		})
	default:
		sort.Slice(processInfos, func(i, j int) bool {
			return processInfos[i].CPU > processInfos[j].CPU
		})
	}

	return processInfos, nil
}

func getStaticInfo() StaticInformation {
	hostInfo, _ := host.Info()
	cpuInfo, _ := cpu.Info()
	memInfo, _ := mem.VirtualMemory()
	diskInfo, _ := disk.Usage("/")
	bootTime := time.Unix(int64(hostInfo.BootTime), 0)
	cpuCount, _ := cpu.Counts(true) // `true` for logical CPUs

	return StaticInformation{
		HostInfo: formatKeyValue("Host", hostInfo.Hostname) + ", " +
			formatKeyValue("Platform", hostInfo.Platform+" "+hostInfo.PlatformVersion) + ", " +
			formatKeyValue("OS", hostInfo.OS) + ", " +
			formatKeyValue("Uptime", time.Duration(hostInfo.Uptime)*time.Second) + ", " +
			formatKeyValue("Boot Time", bootTime.Format("Mon, 02 Jan 2006 15:04:05 MST")) + ", " +
			formatKeyValue("Virtualization", hostInfo.VirtualizationSystem) + ", " +
			formatKeyValue("Role", hostInfo.VirtualizationRole),
		CPUInfo: formatKeyValue("CPU Cores", cpuCount) + ", " +
			formatKeyValue("Specification", cpuInfo[0].ModelName) + ", " +
			formatKeyValue("Cache Size", fmt.Sprintf("%dK", cpuInfo[0].CacheSize)),
		MemoryTotal: formatKeyValue("Memory Total", fmt.Sprintf("%.2fG", float64(memInfo.Total)/(1024*1024*1024))) + ", " +
			formatKeyValue("Buffers", fmt.Sprintf("%.2fG", float64(memInfo.Buffers)/(1024*1024*1024))) + ", " +
			formatKeyValue("Cached", fmt.Sprintf("%.2fG", float64(memInfo.Cached)/(1024*1024*1024))),
		DiskTotal: formatKeyValue("Disk Total (/)", fmt.Sprintf("%.2fG", float64(diskInfo.Total)/(1024*1024*1024))),
	}
}

func getKubernetesInfo() ([]K8sContainerInfo, error) {
	cInfo := make([]K8sContainerInfo, 0)
	if !kubernetesInitialized {
		config, err := getConfig()
		if err != nil {
			return cInfo, err
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return cInfo, err
		}

		clientSet = clientset
		kubernetesInitialized = true
	}

	pods, err := clientSet.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return cInfo, err
	}

	for _, pod := range pods.Items {
		if pod.Namespace != "kube-system" && pod.Namespace != "local-path-storage" {
			for _, container := range pod.Spec.Containers {
				services, _ := getServicesForPod(clientSet, &pod)
				c := K8sContainerInfo{
					PodName:       pod.Name,
					ContainerName: container.Name,
					NodeName:      pod.Spec.NodeName,
					PodIP:         pod.Status.PodIP,
					Services:      services,
					Namespace:     pod.Namespace,
				}
				cInfo = append(cInfo, c)
			}
		}
	}
	return cInfo, nil

}

func getDockerInfo() ([]DockerContainerInfo, error) {
	var stats []DockerContainerInfo

	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}|{{.BlockIO}}|{{.PIDs}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		fmt.Println("Error executing command:", err)
		return stats, err
	}

	output := out.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) < 7 {
			continue
		}

		stat := DockerContainerInfo{
			Name:             fields[0],
			CPUPercentage:    fields[1],
			MemoryUsage:      fields[2],
			MemoryPercentage: fields[3],
		}

		stats = append(stats, stat)
	}

	return stats, nil
}

func formatKeyValue(key string, value interface{}) string {
	return fmt.Sprintf("<span class=\"key\">%s:</span> <span class=\"value\">%v</span>", key, value)
}

func getSystemInfo(screenType string) SystemInfo {
	cpuPercent, _ := cpu.Percent(0, false)
	cpuLoad, _ := load.Avg()
	memStat, _ := mem.VirtualMemory()
	swapStat, _ := mem.SwapMemory()
	diskStat, _ := disk.Usage("/")
	netStats, _ := net.IOCounters(true)
	processes, _ := getTopProcesses(screenType)

	var networkInterfaces []NetworkInterface
	for _, iface := range netStats {
		name := iface.Name
		if strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") {
			continue
		}
		networkInterfaces = append(networkInterfaces, NetworkInterface{
			Name:     name,
			InBytes:  iface.BytesRecv,
			OutBytes: iface.BytesSent,
		})
	}

	s := SystemInfo{
		StaticInfo: getStaticInfo(),
		CPU: CPUInfo{
			Active: cpuPercent[0],
			Idle:   100 - cpuPercent[0],
			AvgLoad: map[string]float64{
				"load1":  cpuLoad.Load1,
				"load5":  cpuLoad.Load5,
				"load15": cpuLoad.Load15,
			},
		},
		Mem: MemInfo{
			Available:   memStat.Available,
			Free:        memStat.Free,
			Used:        memStat.Used,
			UsedPercent: memStat.UsedPercent,
			Buffers:     memStat.Buffers,
			Cached:      memStat.Cached,
		},
		Swap: SwapInfo{
			Total:       swapStat.Total,
			Free:        swapStat.Free,
			Used:        swapStat.Used,
			UsedPercent: swapStat.UsedPercent,
		},
		Disk: DiskInfo{
			Total:       diskStat.Total,
			Free:        diskStat.Free,
			Used:        diskStat.Used,
			UsedPercent: diskStat.UsedPercent,
		},
		Network: NetworkInfo{
			Interfaces: networkInterfaces,
		},
		Processes: processes,
	}

	if screenType == "kubernetes" {
		c, _ := getKubernetesInfo()
		s.K8sContainerInfos = c
	} else if screenType == "docker" {
		c, _ := getDockerInfo()
		s.DockerContainerInfos = c
	}
	return s
}

func getConfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}
	return config, nil
}

func getServicesForPod(clientset *kubernetes.Clientset, pod *v1.Pod) (string, error) {
	services, err := clientset.CoreV1().Services(pod.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	var podServices []string
	for _, svc := range services.Items {
		if svc.Spec.Selector == nil {
			continue
		}
		matches := true
		for k, v := range svc.Spec.Selector {
			if pod.Labels[k] != v {
				matches = false
				break
			}
		}
		if matches {
			podServices = append(podServices, svc.Name)
		}
	}

	return strings.Join(podServices, ", "), nil
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `
	<!DOCTYPE html>
	<html>
	<head>
        <title>System Monitor</title>
        <style>
            body {
                margin: 0;
                padding: 0;
                background-color: #000000;
                color: #ffffff;
                overflow: hidden;
            }
            #staticInfo, #output1, #output2, #output3, #output4, #output5, #output6, #output7, #output8, #output9 {
                position: fixed;
                left: 0;
                width: 100%;
                padding: 5px;
                background-color: #000000;
                font-size: 16px;
                line-height: 1.2;
                white-space: pre-wrap;
            }
            #staticInfo { top: 0; }
            #output1 { top: 100px; }
            #output2 { top: 125px; }
            #output3 { top: 150px; }
            #output4 { top: 175px; }
            #output5 { top: 200px; }
	    #output6 { top: 225px; } /* Disk Space Info */
            #output7 { top: 250px; } /* Disk Bar */
            #output8 { top: 275px; } /* Network Info */
            #output9 { top: 325px; } /* Network Info */

	    #output9 {
		border: 1px solid #00FF00; /* Green border */
		padding: 10px;
		margin-top: 10px;
		background-color: #001100; /* Dark green background */
		box-shadow: 0 0 10px rgba(0, 255, 0, 0.5); /* Green glow effect */
	    }

	    #output9 pre {
		margin: 0;
		white-space: pre-wrap;
		word-wrap: break-word;
	    }

            .key { color: #00FF00; /* True Green */ }
            .value { color: #ffffff; }
            .cpu-bar { color: #0000FF; /* Blue for CPU usage bar */ }
            .mem-bar { color: #0000FF; /* Blue for Memory usage bar */ }
            .swap-bar { color: #0000FF; /* Blue for Memory usage bar */ }
            .disk-bar { color: #0000FF; /* Blue for Memory usage bar */ }
	    .process-line {
                font-family: monospace;
                white-space: pre;
            }
	</style>
	</head>
	<body>
        <pre id="staticInfo" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output1" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output2" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output3" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output4" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output5" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output6" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output7" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output8" style="font-family: Arial, sans-serif;"></pre>
        <pre id="output9" style="font-family: Arial, sans-serif;"></pre>

        <script>

	let currentPage = 0;
	let processCurrentPage = 0;
	const kubernetesPageSize = 5; // Number of records per page
	const dockerPageSize = 5; // Number of records per page
	const processPageSize = 12; // Number of records per page for processes
	let sysData = {}; // This should be populated with your actual data
	let endPoint = 'main'; // Default endpoint

	let infoScrollPosition = 0;
	const infoScrollStep = 5; // Number of lines to scroll per key press
	let currentInfos = [];

	let selectedContainerIndex = -1;
	let waitingForSecondKey = false;
	let selectedNumber = -1;
	let isDisplayingContainerInfo = false;

	function displayLogs(logs) {
		currentInfos = logs.split('\n');
		infoScrollPosition = 0;
		updateInfoDisplay();
	}

	function displayDescription(description) {
		currentInfos = description.split('\n');
		infoScrollPosition = 0;
		updateInfoDisplay();
	}

	function updateInfoDisplay() {
		const visibleLines = currentInfos.slice(infoScrollPosition, infoScrollPosition + 10).join('\n');
		document.getElementById('output9').innerHTML = '<pre>' + visibleLines + '</pre>';
	}


	function handleDownArrow() {
		if (infoScrollPosition + 10 < currentInfos.length) {
			infoScrollPosition = Math.min(currentInfos.length - 10, infoScrollPosition + infoScrollStep);
			updateInfoDisplay();
		}
	}

	function handleUpArrow() {
		if (infoScrollPosition > 0) {
			infoScrollPosition = Math.max(0, infoScrollPosition - infoScrollStep);
			updateInfoDisplay();
		}
	}


	document.addEventListener('keydown', function(event) {
		const key = event.key.toLowerCase();
		if (isDisplayingContainerInfo) {
			if (key === 'escape') {
				isDisplayingContainerInfo = false;
				if (endPoint === "kubernetes") {
					renderKubernetesTable();
				} else if (endPoint === "docker") {
					renderDockerTable();
				}
			}
			if (key === 'arrowdown') {
				event.preventDefault();
				handleDownArrow();
			}
		
			if (key === 'arrowup') {
				event.preventDefault();
				handleUpArrow();
			}
			return;
		}
	
		if (waitingForSecondKey && (endPoint === "kubernetes")) {
			if (key === 'l') {
				viewContainerLogs(selectedNumber);
			} else if (key === 'd') {
				viewContainerDescription(selectedNumber);
			}
			waitingForSecondKey = false;
			selectedNumber = -1;
			return;
		}

		if (waitingForSecondKey && (endPoint === "docker")) {
			if (key === 'l') {
				viewDockerContainerLogs(selectedNumber);
			} else if (key === 'i') {
				viewDockerContainerInspection(selectedNumber);
			}
			waitingForSecondKey = false;
			selectedNumber = -1;
			return;
		}
	
		if (endPoint === "kubernetes" && parseInt(key) >= 1 && parseInt(key) <= 9) {
			selectedNumber = currentPage * kubernetesPageSize + parseInt(key) - 1;
			if (selectedNumber < sysData.k8scontainerinfos.length) {
				highlightSelectedContainer(selectedNumber);
				waitingForSecondKey = true;
			}
			return;
		}

		if (endPoint === "docker" && parseInt(key) >= 1 && parseInt(key) <= 9) {
			selectedNumber = currentPage * dockerPageSize + parseInt(key) - 1;
			if (selectedNumber < sysData.dockercontainerinfos.length) {
				highlightSelectedContainer(selectedNumber);
				waitingForSecondKey = true;
			}
			return;
		}
	
		switch (key) {
		case 'm':
			endPoint = 'memory';
			break;
		case 'c':
		case 'p':
		case 'r':
			endPoint = 'main';
			break;
		case 'n':
			endPoint = 'net';
			break;
	        case 'd':
			endPoint = 'disk';
			break;
		case 'k':
			endPoint = 'kubernetes';
			break;
		case 't':
			endPoint = 'docker';
			break;
		case ' ':
			if (endPoint === "kubernetes") {
				if ((currentPage + 1) * kubernetesPageSize < sysData.k8scontainerinfos.length) {
					currentPage++;
				} else {
					currentPage = 0;
				}
				renderKubernetesTable();
			} else if (endPoint === "docker") {
				if ((currentPage + 1) * dockerPageSize < sysData.dockercontainerinfos.length) {
					currentPage++;
				} else {
					currentPage = 0;
				}
				renderDockerTable();
			} else {
				if ((processCurrentPage + 1) * processPageSize < sysData.processes.length) {
					processCurrentPage++;
				} else {
					processCurrentPage = 0;
				}
				renderProcessTable();
			}
			break;
	        case 'escape':
			if (endPoint === "kubernetes") {
				renderKubernetesTable();
			} else if (endPoint === "docker") {
				renderDockerTable();
			}
			break;
		default:
			return;
		}
	
		// Reset selection and re-render for endpoint changes
		if (['m', 'c', 'p', 'r', 'n', 'd', 'k', 't'].includes(key)) {
			selectedNumber = -1;
			currentPage = 0;
			clearAndRefresh();
		}
	});

	function highlightSelectedContainer(index) {
		document.querySelectorAll('tr').forEach(row => row.classList.remove('selected'));
		if (index !== -1) {
			const selectedRow = document.querySelector("tr[data-container-index='" + (index % kubernetesPageSize) + "']");
			if (selectedRow) {
				selectedRow.classList.add('selected');
			}
		}
	}


	function viewContainerLogs(index) {
		if (index === -1 || index > sysData.k8scontainerinfos.length) return;
		const container = sysData.k8scontainerinfos[index];
		// Fetch and display logs for the selected container
		fetchContainerLogs(container.podName, container.containerName, container.namespace)
		.then(logs => {
			displayLogs(logs)
			isDisplayingContainerInfo = true; 
		});
	}

	function viewContainerDescription(index) {
		if (index === -1 || index >= sysData.k8scontainerinfos.length) return;
		const container = sysData.k8scontainerinfos[index];
		// Fetch and display logs for the selected container
		fetchContainerDescription(container.podName, container.namespace)
		.then(description => {
			displayDescription(description)
			isDisplayingContainerInfo = true; 
		});
	}

	function viewDockerContainerLogs(index) {
		if (index === -1 || index > sysData.dockercontainerinfos.length) return;
		const container = sysData.dockercontainerinfos[index];
		// Fetch and display logs for the selected container
		fetchContainerLogsDocker(container.name)
		.then(logs => {
			displayLogs(logs)
			isDisplayingContainerInfo = true; 
		});
	}

	function viewDockerContainerInspection(index) {
		if (index === -1 || index >= sysData.dockercontainerinfos.length) return;
		const container = sysData.dockercontainerinfos[index];
		// Fetch and display logs for the selected container
		fetchContainerInspectionDocker(container.name)
		.then(description => {
			displayDescription(description)
			isDisplayingContainerInfo = true; 
		});
	}

	function applyBoxStyling(elementId) {
		const element = document.getElementById(elementId);
		element.style.border = '1px solid #00FF00';
		element.style.padding = '10px';
		element.style.marginTop = '10px';
		element.style.backgroundColor = '#001100';
		element.style.boxShadow = '0 0 10px rgba(0, 255, 0, 0.5)';
	}

	function describeContainer(index) {
		if (index === -1 || index > sysData.k8scontainerinfos.length) return;
		const container = sysData.k8scontainerinfos[index];
		// Fetch and display describe information for the selected container
		fetchContainerDescription(econtainer.podName, container.namespace)
		.then(description => {
			displayDescribe(description);
			isDisplayingContainerInfo = true;
		})
		.catch(error => {
			console.error("Error fetching pod description:", error);
			displayDescribe("Error fetching pod description: " + error.message);
			isDisplayingContainerInfo = true;
		});
	}


	function clearAndRefresh() {
		// Clear all output elements
		['staticInfo', 'output1', 'output2', 'output3', 'output4', 'output5', 'output6', 'output7', 'output8', 'output9'].forEach(id => {
			const element = document.getElementById(id);
			element.innerHTML = '';
			// Remove any box styling
			element.style.border = 'none';
			element.style.padding = '0';
			element.style.boxShadow = 'none';
			element.style.backgroundColor = 'transparent';
		});

		// Reset the background color of the body to ensure a clean slate
		document.body.style.backgroundColor = '#000000';

		 // Fetch and display data again
		fetchData();
	}


        function formatBytes(bytes) {
            const units = ['B', 'KB', 'MB', 'GB', 'TB'];
            let i = 0;
            while (bytes >= 1024 && i < units.length - 1) {
                bytes /= 1024;
                i++;
            }
            return bytes.toFixed(2) + ' ' + units[i];
        }

        function createBar(percentage, className) {
            const totalWidth = 30;
            const filledWidth = Math.round(percentage * totalWidth / 100);
            const emptyWidth = totalWidth - filledWidth;

	    let bar;
	    bar = '[' + '<span class="' + className + '">' + '|'.repeat(filledWidth) + ' '.repeat(emptyWidth) + ']';
            return bar;
        }

        function fetchData() {
		  fetch('/api/sysstat/' + endPoint)
                .then(response => response.json())
                .then(data => {
			sysData = data
                    const staticInfo =
                        '<span class="key">' + data.staticInfo.hostInfo + '</span>\n' +
                        '<span class="key">' + data.staticInfo.cpuInfo + '</span>\n' +
                        '<span class="key">' + data.staticInfo.memoryTotal + '</span>\n' +
                        '<span class="key">' + data.staticInfo.diskTotal + '</span>';

                    document.getElementById('staticInfo').innerHTML = staticInfo;

                    const cpuBar = createBar(data.cpu.active, 'cpu-bar');
                    const cpuOutput =
                        '<span class="key">CPU Status:</span> ' +
                        '<span class="key">Active:</span> <span class="value">' + data.cpu.active.toFixed(1) + '%</span>, ' +
                        '<span class="key">Idle:</span> <span class="value">' + data.cpu.idle.toFixed(1) + '%</span>, ' +
                        '<span class="key">Avg Load:</span> <span class="value">{' +
                        '"load1":' + data.cpu.avg_load.load1.toFixed(2) + ',' +
                        '"load5":' + data.cpu.avg_load.load5.toFixed(2) + ',' +
                        '"load15":' + data.cpu.avg_load.load15.toFixed(2) + '}</span>, ' +
                        '<span class="key">CPU:</span> <span class="value">' + cpuBar + ' ' + data.cpu.active.toFixed(1) + '%</span>';
                    document.getElementById('output1').innerHTML = cpuOutput;

                    const memOutput =
                        '<span class="key">Memory:</span> ' +
                        '<span class="key">Available:</span> <span class="value">' + formatBytes(data.mem.available) + '</span>, ' +
                        '<span class="key">Free:</span> <span class="value">' + formatBytes(data.mem.free) + '</span>, ' +
                        '<span class="key">Used:</span> <span class="value">' + formatBytes(data.mem.used) + '</span>, ' +
                        '<span class="key">Used Percent:</span> <span class="value">' + data.mem.usedPercent.toFixed(2) + '%</span>, ' +
                        '<span class="key">Buffers:</span> <span class="value">' + formatBytes(data.mem.buffers) + '</span>, ' +
                        '<span class="key">Cached:</span> <span class="value">' + formatBytes(data.mem.cached) + '</span>';
                    document.getElementById('output2').innerHTML = memOutput;

                    const memBar = createBar(data.mem.usedPercent, 'mem-bar');
                    const memBarOutput =
                        '<div><span class="key">Memory </span>: <span class="value">' + memBar + ' ' + data.mem.usedPercent.toFixed(1) + '%</span></div>';
                    document.getElementById('output3').innerHTML = memBarOutput; // Display Memory Bar

            	// Swap Space Output
            	const swapOutput =
                        '<span class="key">Swap Space:</span> ' +
                        '<span class="key">Total:</span> <span class="value">' + formatBytes(data.swap.total) + '</span>, ' +
                        '<span class="key">Free:</span> <span class="value">' + formatBytes(data.swap.free) + '</span>, ' +
                        '<span class="key">Used:</span> <span class="value">' + formatBytes(data.swap.used) + '</span>, ' +
                        '<span class="key">Used Percent:</span> <span class="value">' + data.swap.usedPercent.toFixed(2) + '%</span>';
		document.getElementById('output4').innerHTML = swapOutput; // Display Swap Space Info
                
		const swapBar = createBar(data.swap.usedPercent, 'swap-bar');
                    const swapBarOutput =
                        '<div><span class="key">Swap     </span>: <span class="value">' + swapBar + ' ' + data.swap.usedPercent.toFixed(1) + '%</span></div>';
		document.getElementById('output5').innerHTML = swapBarOutput; // Display Swap Bar
		// Disk Space Output
            	const diskOutput =
                        '<span class="key">Disk Space:</span> ' +
                        '<span class="key">Total:</span> <span class="value">' + formatBytes(data.disk.total) + '</span>, ' +
                        '<span class="key">Free:</span> <span class="value">' + formatBytes(data.disk.free) + '</span>, ' +
                        '<span class="key">Used:</span> <span class="value">' + formatBytes(data.disk.used) + '</span>, ' +
                        '<span class="key">Used Percent:</span> <span class="value">' + data.disk.usedPercent.toFixed(2) + '%</span>';
		document.getElementById('output6').innerHTML = diskOutput; // Display Swap Space Info

                    const diskBar = createBar(data.disk.usedPercent, 'disk-bar');
                    const diskBarOutput =
                        '<div><span class="key">Disk      </span>: <span class="value">' + diskBar + ' ' + data.disk.usedPercent.toFixed(1) + '%</span></div>';
            	document.getElementById('output7').innerHTML = diskBarOutput; // Display Swap Bar

		// Network Output
		let netOutput = '<span class="key">Network Interfaces:</span> ';
		let ifaceOutputs = []; // Array to hold interface outputs

		for (const iface of data.network.interfaces) {
			ifaceOutputs.push(
				iface.name +
				' In: ' + formatBytes(iface.inBytes) + ', ' +
				' Out: ' + formatBytes(iface.outBytes)
			);
		}

		// Join the outputs with a separator (e.g., " | ")
		netOutput += '<span class="value">' + ifaceOutputs.join(' | ') + '</span>';
		document.getElementById('output8').innerHTML = netOutput; // Display Network Info

		if ((endPoint !== "kubernetes") && (endPoint !== "docker")) {
			processCurrentPage = 0;
			renderProcessTable() 
		} else if (endPoint === "docker") {
			if (!isDisplayingContainerInfo) {
				renderDockerTable() 
			}
		} else {
			if (!isDisplayingContainerInfo) {
                		renderKubernetesTable();
            		}
		}
                })
                .catch(error => {
                    console.error('Error fetching system info:', error);
		    clearScreen();
                    document.getElementById('staticInfo').textContent = 'Error fetching system info';
                    document.getElementById('output1').textContent = 'Error fetching system info';
                    document.getElementById('output2').textContent = 'Error fetching system info';
                    document.getElementById('output3').textContent = 'Error fetching system info';
                    document.getElementById('output4').textContent = 'Error fetching system info';
                    document.getElementById('output5').textContent = 'Error fetching system info';
                    document.getElementById('output6').textContent = 'Error fetching system info';
                    document.getElementById('output7').textContent = 'Error fetching system info';
                    document.getElementById('output8').textContent = 'Error fetching system info';
                    document.getElementById('output9').textContent = 'Error fetching system info';
                });
        }


	function renderKubernetesTable() {
		let data = sysData;

		applyBoxStyling('output9');
		let table = '<table>';

		// Add the title in green at the start of the line
		table = '<div style="color: green; font-weight: bold; margin-bottom: 10px;">Kubernetes Containers:</div>' + table;

		const start = currentPage * kubernetesPageSize;
		const end = start + kubernetesPageSize;
		const paginatedData = data.k8scontainerinfos.slice(start, end);

		paginatedData.forEach((container, index) => {
			const containerNumber = start + index + 1; // Calculate the container number
			table += '<tr>' +
			'<td><span class="key">Container #: </span><span class="value">' + containerNumber + '</span></td>' +
			'<td><span class="key">Pod Name: </span><span class="value">' + container.podName + '</span></td>' +
			'<td><span class="key">Container: </span><span class="value">' + container.containerName + '</span></td>' +
			'<td><span class="key">Namespace: </span><span class="value">' + container.namespace + '</span></td>' +
			'<td><span class="key">PodIP: </span><span class="value">' + container.podIP + '</span></td>' +
			'<td><span class="key">Container Node: </span><span class="value">' + container.nodeName + '</span></td>' +
			'<td><span class="key">Services: </span><span class="value">' + container.services + '</span></td>' +
			'</tr>';
		});

		table += '</table>';
		document.getElementById('output9').innerHTML = table;
	}

	function renderDockerTable() {
		let data = sysData;

		applyBoxStyling('output9');
		let table = '<table>';

		// Add the title in green at the start of the line
		table = '<div style="color: green; font-weight: bold; margin-bottom: 10px;">Docker Containers:</div>' + table;

		const start = currentPage * dockerPageSize;
		const end = start + dockerPageSize;
		const paginatedData = data.dockercontainerinfos.slice(start, end);

		paginatedData.forEach((container, index) => {
			const containerNumber = start + index + 1; // Calculate the container number
			table += '<tr>' +
			'<td><span class="key">Container #: </span><span class="value">' + containerNumber + '</span></td>' +
			'<td><span class="key">Container Name: </span><span class="value">' + container.name + '</span></td>' +
			'<td><span class="key">CPU Percentage: </span><span class="value">' + container.cpuPercentage + '</span></td>' +
			'<td><span class="key">Memory Usage: </span><span class="value">' + container.memoryUsage + '</span></td>' +
			'<td><span class="key">Memory Percentage: </span><span class="value">' + container.memoryPercentage + '</span></td>' +
			'</tr>';
		});

		table += '</table>';
		document.getElementById('output9').innerHTML = table;
	}

	function renderProcessTable() {
		let data = sysData;

		applyBoxStyling('output9');
		let processOutput = '<span class="key">Top Processes:</span>\n';
		const start = processCurrentPage * processPageSize;
		const end = start + processPageSize;
		const paginatedData = data.processes.slice(start, end);
			paginatedData.forEach(process => {
				let processLine = '<div class="process-line">';
				processLine += '<span class="key">PID:</span> <span class="value">' + process.pid.toString().padStart(8) + '</span> ';
				processLine += '<span class="key">CPU:</span> <span class="value">' + process.cpu.toFixed(2).padStart(8) + '</span> ';
				processLine += '<span class="key">Memory:</span> <span class="value">' + process.memory.toFixed(2).padStart(8) + '</span> ';
				if (endPoint === 'disk') {
					processLine += '<span class="key">RdWrMB:</span> <span class="value">' + process.diskio.toString().padStart(10) + '</span> ';
				} else if (endPoint === 'net') {
					processLine += '<span class="key">Sockets:</span> <span class="value">' + process.socketcount.toString().padStart(8) + '</span> ';
				}
				processLine += '<span class="key">RSS:</span> <span class="value">' + formatBytes(process.rss).padStart(10) + '</span> ';
				processLine += '<span class="key">VMS:</span> <span class="value">' + formatBytes(process.vms).padStart(10) + '</span> ';
	
				processLine += '<span class="key">Command:</span> <span class="value">' + (process.command) + '</span>';
				processLine += '</div>';
				processOutput += processLine;
			});
			document.getElementById('output9').innerHTML = processOutput;
	}

	function fetchContainerLogs(podName, containerName, namespace) {
		console.log('Fetching logs for:', podName, containerName, namespace);
		return fetch('/api/sysstat/log?pod=' + encodeURIComponent(podName) + 
			'&container=' + encodeURIComponent(containerName) + 
			'&namespace=' + encodeURIComponent(namespace))
		.then(response => {
			if (!response.ok) {
				throw new Error('Network response was not ok: ' + response.status);
			}
			return response.text();
		})
		.then(text => {
			console.log('Received log data:', text.substring(0, 100) + '...');
			return text;
		})
		.catch(error => {
			console.error('Error fetching container logs:', error);
			return 'Error fetching container logs: ' + error.message;
		});
	}

	function fetchContainerDescription(podName, namespace) {
		console.log('Fetching description for:', podName, namespace);
		return fetch('/api/sysstat/describe?pod=' + encodeURIComponent(podName) +
			'&namespace=' + encodeURIComponent(namespace))
		.then(response => {
			if (!response.ok) {
				throw new Error('Network response was not ok: ' + response.status);
			}
			return response.text();
		})
		.then(text => {
			console.log('Received description data:', text.substring(0, 100) + '...');
			return text;
		})
		.catch(error => {
			console.error('Error fetching container description:', error);
			return 'Error fetching container description: ' + error.message;
		});
	}

	function fetchContainerLogsDocker(containerName) {
		console.log('Fetching logs for:', containerName);
		return fetch('/api/sysstat/dockerlog?container=' + encodeURIComponent(containerName))
		.then(response => {
			if (!response.ok) {
				throw new Error('Network response was not ok: ' + response.status);
			}
			return response.text();
		})
		.then(text => {
			console.log('Received log data:', text.substring(0, 100) + '...');
			return text;
		})
		.catch(error => {
			console.error('Error fetching container logs:', error);
			return 'Error fetching container logs: ' + error.message;
		});
	}

	function fetchContainerInspectionDocker(containerName) {
		console.log('Fetching description for:', containerName);
		return fetch('/api/sysstat/dockerinspection?container=' + encodeURIComponent(containerName))
		.then(response => {
			if (!response.ok) {
				throw new Error('Network response was not ok: ' + response.status);
			}
			return response.text();
		})
		.then(text => {
			console.log('Received description data:', text.substring(0, 100) + '...');
			return text;
		})
		.catch(error => {
			console.error('Error fetching container description:', error);
			return 'Error fetching container description: ' + error.message;
		});
	}

	function clearScreen() {
		['staticInfo', 'output1', 'output2', 'output3', 'output4', 'output5', 'output6', 'output7', 'output8', 'output9'].forEach(id => {
			const element = document.getElementById(id);
			element.innerHTML = '';
			element.style.border = 'none';
			element.style.padding = '0';
			element.style.boxShadow = 'none';
			element.style.backgroundColor = 'transparent';
		});
		document.body.style.backgroundColor = '#000000';
	}

	clearAndRefresh();
	fetchData();
	setInterval(fetchData, 5000);
	</script>
	</body>
	</html>
	`)
}

func handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	podName := r.URL.Query().Get("pod")
	containerName := r.URL.Query().Get("container")
	namespace := r.URL.Query().Get("namespace")

	cmd := exec.Command("kubectl", "logs", "-n", namespace, podName, "-c", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Error fetching logs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(output)
}

func handleDockerContainerLogs(w http.ResponseWriter, r *http.Request) {
	containerName := r.URL.Query().Get("container")

	cmd := exec.Command("docker", "logs", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Error fetching logs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(output)
}

func handleContainerDescribe(w http.ResponseWriter, r *http.Request) {
	podName := r.URL.Query().Get("pod")
	namespace := r.URL.Query().Get("namespace")

	cmd := exec.Command("kubectl", "describe", "pod", "-n", namespace, podName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Error describing pod: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(output)
}

func handleDockerContainerInspection(w http.ResponseWriter, r *http.Request) {
	containerName := r.URL.Query().Get("container")

	cmd := exec.Command("docker", "inspect", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Error describing pod: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(output)
}

func handleAPIKubernetes(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("kubernetes")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func handleAPIDocker(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("docker")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func handleAPIMain(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("main")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func handleAPIMemory(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("memory")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func handleAPIDisk(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("disk")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func handleAPINet(w http.ResponseWriter, r *http.Request) {
	sysStat := getSystemInfo("net")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sysStat)
}

func startServerForBrowserUI(scanInterval, port int) {
	processIOMap = make(map[int32]uint64)
	http.HandleFunc("/", handleRoot)
	http.HandleFunc("/api/sysstat/main", handleAPIMain)
	http.HandleFunc("/api/sysstat/memory", handleAPIMemory)
	http.HandleFunc("/api/sysstat/disk", handleAPIDisk)
	http.HandleFunc("/api/sysstat/net", handleAPINet)
	http.HandleFunc("/api/sysstat/kubernetes", handleAPIKubernetes)
	http.HandleFunc("/api/sysstat/docker", handleAPIDocker)
	http.HandleFunc("/api/sysstat/log", handleContainerLogs)
	http.HandleFunc("/api/sysstat/dockerlog", handleDockerContainerLogs)
	http.HandleFunc("/api/sysstat/describe", handleContainerDescribe)
	http.HandleFunc("/api/sysstat/dockerinspection", handleDockerContainerInspection)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", port), nil))
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
