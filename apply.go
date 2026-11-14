package main

// Root part: started by modbus-proxy-apply.service as soon as the UI has
// placed a request in the spool.
//
// Save: validate -> backup -> replace config -> restart proxy -> check that
// it runs stably and all ports are listening. If not: restore the backup,
// restart again, report the error.
//
// The result goes to <state>/result/status.json. Nothing is written to
// stdout/stderr, the exit code is always 0 (otherwise systemd would log a
// failure to the journal).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const keepBackups = 20

func runApply(p Paths) int {
	for _, d := range []string{p.ResultDir(), p.BackupDir()} {
		_ = os.MkdirAll(d, 0755)
	}
	req, err := takeRequest(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		writeStatus(p, JobStatus{State: "error", Message: "Request not readable: " + err.Error(), Finished: time.Now().Unix()})
		return 0
	}
	started := time.Now().Unix()
	writeStatus(p, JobStatus{ID: req.ID, Action: req.Action, State: "running", Message: "Running …", Started: started})

	var res JobStatus
	switch req.Action {
	case "apply":
		res = doApply(p, req)
	case "start", "restart":
		res = doStart(p, req.Action)
	case "stop":
		res = doStop(p)
	default:
		res = JobStatus{State: "error", Message: "Unknown action \"" + req.Action + "\""}
	}
	res.ID, res.Action, res.Started, res.Finished = req.ID, req.Action, started, time.Now().Unix()
	writeStatus(p, res)
	return 0
}

func failed(format string, a ...any) JobStatus {
	return JobStatus{State: "error", Message: fmt.Sprintf(format, a...)}
}

func doApply(p Paths, req *Request) JobStatus {
	cur, err := os.ReadFile(p.ProxyConfig)
	if err != nil {
		return failed("Proxy config not readable: %v", err)
	}
	if req.BaseHash != hashBytes(cur) {
		return failed("The proxy config has been changed in the meantime. Please reload the page and enter the change again. Nothing was saved.")
	}
	cfg, err := parseProxyConfig(cur)
	if err != nil {
		return failed("The current config cannot be parsed (%v). Nothing was saved.", err)
	}
	devs, errs := normalizeAndValidate(req.Devices, otherListeningPorts(cfg, 0))
	if len(errs) > 0 {
		return failed("Validation failed, nothing was saved: %s", joinFieldErrors(errs))
	}
	newData := renderProxyConfig(cfg.Header, devs)
	if bytes.Equal(newData, cur) {
		return JobStatus{State: "ok", Message: "No change – the proxy was not restarted."}
	}

	backup, err := writeBackup(p, cur)
	if err != nil {
		return failed("Backup failed, nothing was changed: %v", err)
	}
	if err := replaceFile(p.ProxyConfig, newData); err != nil {
		return JobStatus{State: "error", Backup: backup, Message: fmt.Sprintf("Config could not be written: %v", err)}
	}

	since := time.Now().Add(-time.Second)
	err = systemctl("restart", p.ProxyUnit)
	if err == nil {
		err = waitHealthy(p.ProxyUnit, listenPorts(devs))
	}
	if err == nil {
		return JobStatus{State: "ok", Backup: backup,
			Message: fmt.Sprintf("Saved. The proxy is running with %d devices.", len(devs))}
	}

	// Rollback
	reason := err.Error()
	detail := journalTail(p.ProxyUnit, 15, since)
	if rerr := replaceFile(p.ProxyConfig, cur); rerr != nil {
		return JobStatus{State: "error", Backup: backup, Detail: detail, Message: fmt.Sprintf(
			"New config failed (%s), and restoring the old one failed as well: %v. Backup: %s", reason, rerr, backup)}
	}
	rerr := systemctl("restart", p.ProxyUnit)
	if rerr == nil {
		rerr = waitHealthy(p.ProxyUnit, listenPorts(cfg.Devices))
	}
	if rerr != nil {
		return JobStatus{State: "error", Backup: backup, Detail: detail, Message: fmt.Sprintf(
			"New config failed (%s). The old config has been restored, but the proxy is still not running: %v", reason, rerr)}
	}
	return JobStatus{State: "rolledback", Backup: backup, Detail: detail, Message: fmt.Sprintf(
		"New config failed: %s. The old config was restored automatically, the proxy is running again.", reason)}
}

func currentPorts(p Paths) []int {
	data, err := os.ReadFile(p.ProxyConfig)
	if err != nil {
		return nil
	}
	cfg, err := parseProxyConfig(data)
	if err != nil {
		return nil
	}
	return listenPorts(cfg.Devices)
}

func doStart(p Paths, action string) JobStatus {
	since := time.Now().Add(-time.Second)
	err := systemctl(action, p.ProxyUnit)
	if err == nil {
		err = waitHealthy(p.ProxyUnit, currentPorts(p))
	}
	if err != nil {
		return JobStatus{State: "error", Message: err.Error(), Detail: journalTail(p.ProxyUnit, 15, since)}
	}
	if action == "start" {
		return JobStatus{State: "ok", Message: "Proxy started."}
	}
	return JobStatus{State: "ok", Message: "Proxy restarted."}
}

func doStop(p Paths) JobStatus {
	if err := systemctl("stop", p.ProxyUnit); err != nil {
		return JobStatus{State: "error", Message: err.Error()}
	}
	m, err := systemctlShow(p.ProxyUnit)
	if err == nil && m["ActiveState"] != "inactive" && m["ActiveState"] != "failed" {
		return JobStatus{State: "error", Message: "Proxy is in state \"" + m["ActiveState"] + "\" after stopping"}
	}
	return JobStatus{State: "ok", Message: "Proxy stopped. No device is reachable through the proxy now."}
}

// waitHealthy waits until the service is running, all ports are listening
// and that stays so for 2 seconds with the same PID.
func waitHealthy(unit string, ports []int) error {
	deadline := time.Now().Add(12 * time.Second)
	var stablePID string
	var stableSince time.Time
	last := "no status yet"
	for time.Now().Before(deadline) {
		m, err := systemctlShow(unit)
		if err != nil {
			last = err.Error()
		} else {
			active, sub := m["ActiveState"], m["SubState"]
			switch {
			case active == "failed":
				return fmt.Errorf("the service failed (%s)", m["Result"])
			case sub == "auto-restart":
				return fmt.Errorf("the proxy exited right after starting")
			case active == "active" && sub == "running":
				if miss := missingPorts(ports); len(miss) > 0 {
					last = fmt.Sprintf("ports not listening: %v", miss)
					stablePID = ""
				} else if stablePID != m["MainPID"] {
					stablePID, stableSince = m["MainPID"], time.Now()
					last = "running, checking stability"
				} else if time.Since(stableSince) >= 2*time.Second {
					return nil
				}
			default:
				last = active + "/" + sub
				stablePID = ""
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("the proxy was not ready after 12 s (%s)", last)
}

func writeBackup(p Paths, data []byte) (string, error) {
	if err := os.MkdirAll(p.BackupDir(), 0755); err != nil {
		return "", err
	}
	base := filepath.Join(p.BackupDir(), "modbus-proxy.yml."+time.Now().Format("20060102-150405"))
	name := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.WriteFile(name, data, 0644); err != nil {
		return "", err
	}
	pruneBackups(p)
	return name, nil
}

func pruneBackups(p Paths) {
	entries, err := os.ReadDir(p.BackupDir())
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "modbus-proxy.yml.") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keepBackups {
		_ = os.Remove(filepath.Join(p.BackupDir(), names[0]))
		names = names[1:]
	}
}

// replaceFile replaces a file atomically and keeps mode and owner.
func replaceFile(path string, data []byte) error {
	mode := os.FileMode(0644)
	uid, gid, haveOwner := 0, 0, false
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		uid, gid, haveOwner = fileOwner(fi)
	}
	tmp := path + ".modbusui-tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, mode)
	if haveOwner {
		_ = os.Chown(tmp, uid, gid)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
