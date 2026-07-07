package main

import (
	"runtime"
	"runtime/debug"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// SystemInfo mirrors plakman's contract.EdgeSystemInfo (duplicated to keep the
// edge free of plakman deps). It's the host facts reported at enrollment.
type SystemInfo struct {
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	NumCPU        int    `json:"num_cpu,omitempty"`
	GoVersion     string `json:"go_version,omitempty"`
	TotalMemory   uint64 `json:"total_memory,omitempty"`
	Kernel        string `json:"kernel,omitempty"`
	KlosetVersion string `json:"kloset_version,omitempty"`
}

// gatherSystemInfo collects the host facts for enrollment. Best-effort: fields
// that can't be determined are left zero rather than failing enrollment.
func gatherSystemInfo() SystemInfo {
	si := SystemInfo{
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		GoVersion:     runtime.Version(),
		KlosetVersion: klosetVersion(),
	}
	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		si.TotalMemory = vm.Total
	}
	// KernelVersion is a cheap OS/kernel string (e.g. "25.5.0" on darwin,
	// the kernel release on linux).
	if kv, err := host.KernelVersion(); err == nil {
		si.Kernel = kv
	}
	return si
}

// klosetVersion reads the kloset module version from the build info, so the
// control plane knows the data-format library this edge is built against.
func klosetVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/PlakarKorp/kloset" {
			return dep.Version
		}
	}
	return ""
}
