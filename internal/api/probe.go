package api

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// checkPort tests if a port is available. Returns nil if free,
// or an error with details about what's using it.
func checkPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		process := identifyProcess(port)
		if process != "" {
			return fmt.Errorf("port %d is in use by %s", port, process)
		}
		return fmt.Errorf("port %d is in use", port)
	}
	_ = ln.Close()
	return nil
}

// identifyProcess tries to find which process is using a port.
func identifyProcess(port int) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	pid := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	nameOut, err := exec.Command("ps", "-p", pid, "-o", "comm=").Output()
	if err != nil {
		return "PID " + pid
	}
	name := strings.TrimSpace(string(nameOut))
	if name != "" {
		return fmt.Sprintf("%s (PID %s)", name, pid)
	}
	return "PID " + pid
}

// suggestPorts returns a list of common alternative ports, skipping any that are in use.
func suggestPorts(exclude int) []int {
	candidates := []int{8888, 8080, 3001, 9000, 9090, 4000}
	var available []int
	for _, p := range candidates {
		if p == exclude {
			continue
		}
		if checkPort(p) == nil {
			available = append(available, p)
		}
	}
	return available
}
