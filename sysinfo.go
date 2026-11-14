package main

// System values: disk, journal, /var/log, memory, uptime, journal lines.

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UnitLines struct {
	Unit  string `json:"unit"`
	Label string `json:"label"`
	Lines int    `json:"lines"`
	Error string `json:"error,omitempty"`
}

type SystemInfo struct {
	Hostname      string      `json:"hostname"`
	DiskTotal     uint64      `json:"diskTotal"`
	DiskUsed      uint64      `json:"diskUsed"`
	DiskAvail     uint64      `json:"diskAvail"`
	DiskError     string      `json:"diskError,omitempty"`
	JournalBytes  int64       `json:"journalBytes"`
	JournalError  string      `json:"journalError,omitempty"`
	VarLogBytes   int64       `json:"varLogBytes"`
	VarLogPartial bool        `json:"varLogPartial"`
	MemTotal      int64       `json:"memTotal"`
	MemAvailable  int64       `json:"memAvailable"`
	UptimeSec     int64       `json:"uptimeSec"`
	Load          string      `json:"load"`
	Lines24h      []UnitLines `json:"lines24h"`
	ProxyTail     string      `json:"proxyTail"`
	Version       string      `json:"version"`
	GoVersion     string      `json:"goVersion"`
	Time          int64       `json:"time"`
}

func systemInfo(p Paths) SystemInfo {
	si := SystemInfo{Version: version, GoVersion: runtime.Version(), Time: time.Now().Unix()}
	si.Hostname, _ = os.Hostname()

	if total, free, avail, err := diskUsage("/"); err != nil {
		si.DiskError = err.Error()
	} else {
		si.DiskTotal, si.DiskUsed, si.DiskAvail = total, total-free, avail
	}

	var jErr error
	for _, d := range []string{"/var/log/journal", "/run/log/journal"} {
		n, _, err := dirSize(d, true)
		if err != nil && !os.IsNotExist(err) {
			jErr = err
		}
		si.JournalBytes += n
	}
	if jErr != nil {
		si.JournalError = jErr.Error()
	}
	si.VarLogBytes, si.VarLogPartial, _ = dirSize("/var/log", false)

	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseInt(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				si.MemTotal = v * 1024
			case "MemAvailable:":
				si.MemAvailable = v * 1024
			}
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			si.UptimeSec = int64(v)
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(data)); len(f) >= 3 {
			si.Load = strings.Join(f[:3], " ")
		}
	}

	units := []UnitLines{
		{Unit: p.ProxyUnit, Label: "Proxy"},
		{Unit: "modbusui.service", Label: "Web UI"},
		{Unit: "modbus-proxy-apply.service", Label: "Request execution"},
	}
	var wg sync.WaitGroup
	for i := range units {
		wg.Add(1)
		go func(u *UnitLines) {
			defer wg.Done()
			n, err := journalCount(u.Unit, "-24h")
			u.Lines = n
			if err != nil {
				u.Error = err.Error()
			}
		}(&units[i])
	}
	wg.Wait()
	si.Lines24h = units
	si.ProxyTail = journalTail(p.ProxyUnit, 20, time.Time{})
	return si
}

// dirSize sums up file sizes. allocated=true counts allocated blocks (like
// journalctl --disk-usage), otherwise the apparent size (like du -sb).
// partial=true means: some directories could not be read.
func dirSize(root string, allocated bool) (int64, bool, error) {
	if _, err := os.Stat(root); err != nil {
		return 0, false, err
	}
	var total int64
	partial := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			partial = true
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				partial = true
				return nil
			}
			if allocated {
				total += allocatedBytes(fi)
			} else {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, partial, err
}
