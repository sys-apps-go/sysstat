go build sysstat.go 

sudo ./sysstat  [Default is 5 second interval for consecutive scans] 
[sudo ./sysstat  -i 10 -k <kube config file>]

Main screen will print the System CPU, Memory, Disk, Network and Process status:

Host: linuxsys, Platform: linuxmint 21.2, 5.15.0-117-generic, OS: linux, Boot Time: Tue, 13 Aug 2024 19:52:53 CDT, Uptime: 1:10:18, Virtualization: kvm, Role: host
CPU Cores: 2, Specification: Intel(R) Celeron(R) N4500 @ 1.10GHz @ 2.80GHz - 0.00/2.80GHz, Cache Size: 4096K
Memory Total: 3.60G, Buffers: 0.06G, Cached: 1.23G
Disk Total: (/): 99.72G (/boot/efi): 0.50G (/media/subham/Pop_OS 22.04 amd64 Intel): 2.47G (/media/subham/writable): 11.80G 

CPU Status: Active: 8.2%, Idle: 91.8%, Avg Load: {"load1":0.2,"load5":0.15,"load15":0.06},  CPU: [||                              8.2%]

Memory Available: 2.64G, Free: 1.68G, Used: 0.63G, Used Percent: 17.49, Buffers: 0.06G, Cached: 1.23G
MEM [|||||                          17.5%]

Swap Space Total: 4.00G, Free: 3.66G, Used: 0.34G, Used Percent: 8.50
SWAP [||                              8.5%]

Disk Space(/): Free: 16.56G, Used: 78.05G, Used Percent: 82.49, ReadCount: 0, WriteCount: 0, IopsInProgress: 0, ReadBytes: 0.00M, WriteBytes: 0.00M
Disk(/) [||||||||||||||||||||||||       82.5%]

NET: [lo]  In: 1.70M  Out: 1.70M, NET: [wlo1]  In: 0.00M  Out: 1.70M, NET: [br-e3f0a5010a5c]  In: 0.00M  Out: 1.70M, NET: [br-0bc670409b18]  In: 0.00M  Out: 1.70M, NET: [docker0]  In: 0.00M  Out: 1.70M, NET: [br-3a143d0b8e26]  In: 0.00M  Out: 1.70M, NET: [br-3d4e48756917]  In: 0.00M  Out: 1.70M, NET: [br-a883597650c3]  In: 0.00M  Out: 1.70M 

Total Processes: 223, Containers Running: None

PID: 5301  CPU: 6.34  %  Memory: 0.73  %  RSS: 27.00M   VMS: 1.69G    Port:       Command: ./sysstat                                                   
PID: 781   CPU: 1.28  %  Memory: 1.82  %  RSS: 67.00M   VMS: 286.00M  Port:       Command: /usr/sbin/netdata -D                                        
PID: 1020  CPU: 1.22  %  Memory: 0.13  %  RSS: 4.00M    VMS: 54.00M   Port:       Command: /usr/lib/netdata/plugins.d/apps.plugin 1                    
PID: 5299  CPU: 0.70  %  Memory: 0.17  %  RSS: 6.00M    VMS: 15.00M   Port:       Command: sudo ./sysstat                                              
PID: 1961  CPU: 0.35  %  Memory: 2.24  %  RSS: 82.00M   VMS: 3.89G    Port:       Command: cinnamon --replace                                          
PID: 2304  CPU: 0.34  %  Memory: 0.62  %  RSS: 22.00M   VMS: 395.00M  Port:       Command: mintreport-tray                                             
PID: 847   CPU: 0.25  %  Memory: 0.78  %  RSS: 28.00M   VMS: 841.00M  Port:       Command: /usr/lib/xorg/Xorg -core :0 -seat seat0 -auth /var/run/lightdm/root/:0 -nolistPID: 2136  CPU: 0.16  %  Memory: 0.45  %  RSS: 16.00M   VMS: 309.00M  Port:       Command: /usr/bin/python3 /usr/bin/blueman-tray                      

Type c for CPU status, m for memory status, p for process status, d for disk status, t for docker instances, r for main screen, q to quit

CPU:

CPU Cores: 2, Specification: Intel(R) Celeron(R) N4500 @ 1.10GHz @ 2.80GHz - 0.00/2.80GHz, Cache Size: 4096K
CPU Status: {"cpu":"cpu-total","user":156.8,"system":132.8,"idle":8380.3,"nice":0.0,"iowait":36.5,"irq":0.0,"softirq":11.2,"steal":0.0,"guest":0.0,"guestNice":0.0}
CPU Status: Active: 3.0%  Idle: 97.0% Average Load: {"load1":0.04,"load5":0.1,"load15":0.06} 

CPU Core : 1: [|                                         3.0%]    CPU Core : 2: [|                                         3.0%]

Total Processes: 220, Containers Running: None

PID: 5343  CPU: 5.15  %  Memory: 0.71  %  RSS: 26.00M   VMS: 1.77G    Port:       Command: ./sysstat                                                   
PID: 781   CPU: 1.28  %  Memory: 1.82  %  RSS: 67.00M   VMS: 286.00M  Port:       Command: /usr/sbin/netdata -D                                        
PID: 1020  CPU: 1.23  %  Memory: 0.13  %  RSS: 4.00M    VMS: 54.00M   Port:       Command: /usr/lib/netdata/plugins.d/apps.plugin 1                    
PID: 1961  CPU: 0.41  %  Memory: 2.20  %  RSS: 81.00M   VMS: 3.89G    Port:       Command: cinnamon --replace                                          
PID: 2304  CPU: 0.34  %  Memory: 0.62  %  RSS: 22.00M   VMS: 395.00M  Port:       Command: mintreport-tray                                             
PID: 847   CPU: 0.31  %  Memory: 0.82  %  RSS: 30.00M   VMS: 841.00M  Port:       Command: /usr/lib/xorg/Xorg -core :0 -seat seat0 -auth /var/run/lightdm/root/:0 -nolistPID: 5341  CPU: 0.29  %  Memory: 0.17  %  RSS: 6.00M    VMS: 15.00M   Port:       Command: sudo ./sysstat                                              
PID: 2191  CPU: 0.23  %  Memory: 0.76  %  RSS: 27.00M   VMS: 477.00M  Port:       Command: /usr/libexec/gnome-terminal-server          

Memory:

Memory Total: 3.60G, Buffers: 0.07G, Cached: 1.23G
Memory Available: 2.62G, Free: 1.66G, Used: 0.65G, Used Percent: 17.92, Buffers: 0.07G, Cached: 1.23G
MEM [=====>                          17.9%]

Swap Space Total: 4.00G, Free: 3.66G, Used: 0.34G, Used Percent: 8.49
SWAP [==>                              8.5%]

Total Processes: 220
Containers Running: None

PID: 1961  CPU: 0.45  %  Memory: 2.20  %  RSS: 81.00M   VMS: 3.89G    Port:       Command: cinnamon --replace                                          
PID: 781   CPU: 1.28  %  Memory: 1.82  %  RSS: 67.00M   VMS: 286.00M  Port:       Command: /usr/sbin/netdata -D                                        
PID: 2253  CPU: 0.01  %  Memory: 1.37  %  RSS: 50.00M   VMS: 729.00M  Port:       Command: mintUpdate                                                  
PID: 932   CPU: 0.04  %  Memory: 1.04  %  RSS: 38.00M   VMS: 2.38G    Port:       Command: /usr/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock
PID: 347   CPU: 0.01  %  Memory: 1.03  %  RSS: 37.00M   VMS: 103.00M  Port:       Command: /lib/systemd/systemd-journald                               
PID: 847   CPU: 0.35  %  Memory: 0.86  %  RSS: 31.00M   VMS: 841.00M  Port:       Command: /usr/lib/xorg/Xorg -core :0 -seat seat0 -auth /var/run/lightdm/root/:0 -nolistPID: 2010  CPU: 0.03  %  Memory: 0.84  %  RSS: 30.00M   VMS: 1004.00M Port:       Command: nemo-desktop                                                
PID: 1830  CPU: 0.01  %  Memory: 0.78  %  RSS: 28.00M   VMS: 443.00M  Port:       Command: csd-automount   

Process:
Total Processes: 220, Containers Running: None

PID: 5505  CPU: 7.44  %  Memory: 0.74  %  RSS: 27.00M   VMS: 1.77G    Port:       Command: ./sysstat                                                   
PID: 781   CPU: 1.28  %  Memory: 1.82  %  RSS: 67.00M   VMS: 286.00M  Port:       Command: /usr/sbin/netdata -D                                        
PID: 1020  CPU: 1.23  %  Memory: 0.13  %  RSS: 4.00M    VMS: 54.00M   Port:       Command: /usr/lib/netdata/plugins.d/apps.plugin 1                    
PID: 1961  CPU: 0.47  %  Memory: 2.20  %  RSS: 81.00M   VMS: 3.89G    Port:       Command: cinnamon --replace                                          
PID: 847   CPU: 0.36  %  Memory: 0.86  %  RSS: 31.00M   VMS: 841.00M  Port:       Command: /usr/lib/xorg/Xorg -core :0 -seat seat0 -auth /var/run/lightdm/root/:0 -nolistPID: 2304  CPU: 0.34  %  Memory: 0.62  %  RSS: 22.00M   VMS: 395.00M  Port:       Command: mintreport-tray                                             
PID: 2191  CPU: 0.29  %  Memory: 0.78  %  RSS: 28.00M   VMS: 477.00M  Port:       Command: /usr/libexec/gnome-terminal-server                          
PID: 2136  CPU: 0.16  %  Memory: 0.45  %  RSS: 16.00M   VMS: 309.00M  Port:       Command: /usr/bin/python3 /usr/bin/blueman-tray                      
PID: 4839  CPU: 0.15  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/0:2-events                                          
PID: 800   CPU: 0.06  %  Memory: 0.61  %  RSS: 22.00M   VMS: 1.86G    Port:       Command: /usr/bin/containerd                                         
PID: 779   CPU: 0.06  %  Memory: 0.45  %  RSS: 16.00M   VMS: 346.00M  Port: 5353  Command: /usr/bin/python3 /usr/bin/glances -s -B 127.0.0.1           
PID: 92    CPU: 0.06  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kswapd0                                                     
PID: 5110  CPU: 0.04  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/0:0H-events_highpri                                 
PID: 103   CPU: 0.04  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/1:1H-kblockd                                        
PID: 4887  CPU: 0.04  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/u4:2-events_unbound                                 
PID: 932   CPU: 0.04  %  Memory: 1.04  %  RSS: 38.00M   VMS: 2.38G    Port:       Command: /usr/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock
PID: 2010  CPU: 0.03  %  Memory: 0.84  %  RSS: 30.00M   VMS: 1004.00M Port:       Command: nemo-desktop                                                
PID: 4816  CPU: 0.03  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/1:0-events                                          
PID: 4869  CPU: 0.03  %  Memory: 0.00  %  RSS: 0.00M    VMS: 0.00M    Port:       Command: kworker/1:3-events       

Containers:

Docker Container Info: Containers Running: 5, Layers Size: 7.35 GB:

1. Image: apachepulsar/pulsar:latest, ID: 0ac273c6a0067e974a494deaa8105e14834aad65f4d2b74dd5a0547435f4c8bf, Names: [/condescending_jackson], Command: bin/pulsar standalone, RootFS Size: 910.39 MB, Created: 2024-08-13T21:08:33-05:00, State: running, Status: Up 22 seconds, Mounts: , PortInfo: 8085/tcp 8085/tcp 6650/tcp 6650/tcp 

....

Kubernetes:


Number of Worker Nodes: 2: 1.Name: kind-worker, IP: 172.19.0.2, 2.Name: kind-worker2, IP: 172.19.0.3


Number of Namespaces: 5: default, kube-node-lease, kube-public, kube-system, local-path-storage

Number of Pods(including system): 20: User Pods: 

 1. Pod Name: flask-deployment-fd76d55fb-bln9c, Container: flask, Namespace: default, PodIP: 10.244.2.2, Container Node: kind-worker
 2. Pod Name: mosquitto-7b75bb5f45-h4s7z, Container: mosquitto, Namespace: default, PodIP: 10.244.1.2, Container Node: kind-worker2
 3. Pod Name: multi-container-deployment-d489f7557-dpp5c, Container: busybox, Namespace: default, PodIP: 10.244.2.4, Container Node: kind-worker
 4. Pod Name: multi-container-deployment-d489f7557-dpp5c, Container: nginx, Namespace: default, PodIP: 10.244.2.4, Container Node: kind-worker
 5. Pod Name: nginx-deployment-67857d48f6-g5vzm, Container: nginx, Namespace: default, PodIP: 10.244.1.4, Container Node: kind-worker2
 6. Pod Name: postgres-deployment-598f65848d-lh24r, Container: postgres, Namespace: default, PodIP: 10.244.1.3, Container Node: kind-worker2
 7. Pod Name: redis-deployment-6b5bcbb6b6-j78nk, Container: redis, Namespace: default, PodIP: 10.244.2.3, Container Node: kind-worker
 8. Pod Name: simple-container-deployment-8b4f856c4-69c6m, Container: alpine, Namespace: default, PodIP: 10.244.1.5, Container Node: kind-worker2


Number of Services(including system): 3: kubernetes (Namespace: default, ClusterIP: 10.96.0.1, Port: 443/TCP), mosquitto (Namespace: default, ClusterIP: 10.96.78.52, Port: 1883/TCP)


Number of PersistentVolumeClaims: 0: 


Number of StorageClasses: 1: standard


Type pod number and l for log contents or i for interactive mode, space for next page, k for kubernetes refresh, r for main screen, q to quit
