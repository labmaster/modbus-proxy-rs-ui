package main

// Exchange between the web UI (user modbusui) and the executor (root):
//
//	UI   writes  <state>/spool/request.json   (directory owned by modbusui)
//	root writes  <state>/result/status.json   (directory owned by root, world-readable)
//
// The path unit starts the root executor as soon as request.json exists.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Request struct {
	ID       string   `json:"id"`
	Action   string   `json:"action"` // apply | start | stop | restart
	Devices  []Device `json:"devices,omitempty"`
	BaseHash string   `json:"baseHash,omitempty"`
	Created  int64    `json:"created"`
}

type JobStatus struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	State    string `json:"state"` // queued | running | ok | rolledback | error | unknown
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Backup   string `json:"backup,omitempty"`
	Started  int64  `json:"started,omitempty"`
	Finished int64  `json:"finished,omitempty"`
}

const maxRequestBytes = 256 * 1024

var (
	errBusy = errors.New("Another request is already running. Please wait a moment.")
	jobIDRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

func newJobID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readStatus(p Paths) (*JobStatus, error) {
	data, err := os.ReadFile(p.StatusFile())
	if err != nil {
		return nil, err
	}
	var st JobStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// readQueued reads our own request that has not been picked up yet (UI side).
func readQueued(p Paths) (*Request, error) {
	f, err := openNoFollow(p.RequestFile())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r Request
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// submitRequest atomically places a request in the spool (UI side).
func submitRequest(p Paths, req Request) error {
	if _, err := os.Lstat(p.RequestFile()); err == nil {
		return errBusy
	}
	if st, err := readStatus(p); err == nil && st.State == "running" &&
		time.Since(time.Unix(st.Started, 0)) < 2*time.Minute {
		return errBusy
	}
	req.Created = time.Now().Unix()
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.SpoolDir(), ".request.tmp")
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p.RequestFile())
}

// takeRequest picks up the request and deletes it immediately (root side).
// Symlinks and non-regular files are rejected.
func takeRequest(p Paths) (*Request, error) {
	path := p.RequestFile()
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		_ = os.Remove(path)
		return nil, err
	}
	_ = os.Remove(path) // remove at once, otherwise the path unit triggers again
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxRequestBytes {
		return nil, errors.New("request too large")
	}
	var r Request
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return nil, errors.New("request is not valid JSON")
	}
	if !jobIDRe.MatchString(r.ID) {
		return nil, errors.New("invalid request ID")
	}
	return &r, nil
}

func writeStatus(p Paths, st JobStatus) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := p.StatusFile() + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Chmod(tmp, 0644)
	_ = os.Rename(tmp, p.StatusFile())
}
