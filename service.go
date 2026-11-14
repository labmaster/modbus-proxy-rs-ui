package main

// Calling systemctl and journalctl. Both are part of systemd.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type ServiceStatus struct {
	Unit      string `json:"unit"`
	Load      string `json:"load"`
	Active    string `json:"active"`
	Sub       string `json:"sub"`
	Result    string `json:"result"`
	Enabled   string `json:"enabled"`
	MainPID   int    `json:"mainPid"`
	NRestarts int    `json:"nRestarts"`
	Since     int64  `json:"since"`     // Unix time since when the unit is "active"
	UptimeSec int64  `json:"uptimeSec"` // 0 if not active
	RSSBytes  int64  `json:"rssBytes"`
	Error     string `json:"error,omitempty"`
}

func cmdEnv() []string {
	return append(os.Environ(), "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0", "LC_ALL=C")
}

func systemctlShow(unit string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "show", "--timestamp=unix", "--no-pager",
		"-p", "LoadState,ActiveState,SubState,Result,MainPID,NRestarts,ActiveEnterTimestamp,UnitFileState",
		"--", unit)
	cmd.Env = cmdEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %v", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

func parseUnixTS(v string) int64 {
	v = strings.TrimPrefix(strings.TrimSpace(v), "@")
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func serviceStatus(unit string) ServiceStatus {
	st := ServiceStatus{Unit: unit}
	m, err := systemctlShow(unit)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Load = m["LoadState"]
	st.Active = m["ActiveState"]
	st.Sub = m["SubState"]
	st.Result = m["Result"]
	st.Enabled = m["UnitFileState"]
	st.MainPID, _ = strconv.Atoi(m["MainPID"])
	st.NRestarts, _ = strconv.Atoi(m["NRestarts"])
	if st.Active == "active" {
		st.Since = parseUnixTS(m["ActiveEnterTimestamp"])
		if st.Since > 0 {
			st.UptimeSec = time.Now().Unix() - st.Since
		}
	}
	if st.MainPID > 0 {
		st.RSSBytes = processRSS(st.MainPID)
	}
	return st
}

func processRSS(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// systemctl runs start/stop/restart (only in -apply mode as root).
func systemctl(action, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", action, "--", unit)
	cmd.Env = cmdEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("systemctl %s: %s", action, msg)
	}
	return nil
}

// journalCount counts the journal entries of a unit since "since"
// (e.g. "-24h"). In JSON output every entry is exactly one line.
func journalCount(unit, since string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unit, "--since", since,
		"-o", "json", "--output-fields=_PID", "--no-pager", "-q")
	cmd.Env = cmdEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	n := 0
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return n, fmt.Errorf("%s", msg)
	}
	return n, nil
}

// journalTail returns the last n lines of a unit, optionally since "since".
func journalTail(unit string, n int, since time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-u", unit, "-n", strconv.Itoa(n), "-o", "short-iso", "--no-pager", "-q"}
	if !since.IsZero() {
		args = append(args, "--since", fmt.Sprintf("@%d", since.Unix()))
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Env = cmdEnv()
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil && s == "" {
		return "journalctl: " + err.Error()
	}
	return s
}
