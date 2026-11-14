package main

// HTTP server of the UI (runs as user modbusui).

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type server struct {
	p        Paths
	uiPort   int
	sessions *sessionStore
	loginMu  sync.Mutex // PBKDF2 one at a time, not in parallel
	submitMu sync.Mutex
	hosts    *hostCache
}

func runServer(p Paths, listen string) int {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modbusui: invalid -listen address:", err)
		return 2
	}
	uiPort, _ := strconv.Atoi(portStr)
	s := &server{p: p, uiPort: uiPort, sessions: newSessionStore(), hosts: newHostCache()}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fmt.Fprintln(os.Stderr, "modbusui:", err)
		return 1
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/devices", s.auth(s.handleDevices))
	mux.HandleFunc("POST /api/devices", s.auth(s.handleSaveDevices))
	mux.HandleFunc("GET /api/job", s.auth(s.handleJob))
	mux.HandleFunc("DELETE /api/job", s.auth(s.handleCancelJob))
	mux.HandleFunc("GET /api/service", s.auth(s.handleService))
	mux.HandleFunc("POST /api/service", s.auth(s.handleServiceAction))
	mux.HandleFunc("GET /api/connections", s.auth(s.handleConnections))
	mux.HandleFunc("POST /api/modbus", s.auth(s.handleModbus))
	mux.HandleFunc("GET /api/system", s.auth(s.handleSystem))

	srv := &http.Server{
		Handler:           securityHeaders(mux),
		ErrorLog:          log.New(io.Discard, "", 0),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modbusui:", err)
		return 1
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "modbusui:", err)
		return 1
	}
	return 0
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	return json.NewDecoder(r.Body).Decode(v)
}

// csrfOK: writing requests must carry the X-Modbusui header. A foreign web
// page cannot set it without a CORS permission.
func csrfOK(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	return r.Header.Get("X-Modbusui") == "1"
}

func (s *server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.sessions.valid(c.Value) {
			writeErr(w, http.StatusUnauthorized, "Please log in.")
			return
		}
		if !csrfOK(r) {
			writeErr(w, http.StatusForbidden, "Request rejected.")
			return
		}
		h(w, r)
	}
}

// --- Login ---

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	ok := err == nil && s.sessions.valid(c.Value)
	_, perr := os.Stat(s.p.PasswordFile)
	writeJSON(w, http.StatusOK, map[string]any{"loggedIn": ok, "passwordSet": perr == nil, "version": version})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		writeErr(w, http.StatusForbidden, "Request rejected.")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	if s.sessions.tooManyFails() {
		writeErr(w, http.StatusTooManyRequests, "Too many failed attempts. Please wait a minute.")
		return
	}
	s.loginMu.Lock()
	ok, err := checkPassword(s.p.PasswordFile, in.Password)
	s.loginMu.Unlock()
	if errors.Is(err, errNoPassword) {
		writeErr(w, http.StatusServiceUnavailable, "No password has been set yet. On the server as root: modbusui -set-password")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Password check failed: "+err.Error())
		return
	}
	if !ok {
		s.sessions.addFail()
		time.Sleep(time.Second)
		writeErr(w, http.StatusUnauthorized, "Wrong password.")
		return
	}
	tok := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		writeErr(w, http.StatusForbidden, "Request rejected.")
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- devices ---

type devicesResp struct {
	Devices  []Device       `json:"devices"`
	Hash     string         `json:"hash"`
	ReadOnly bool           `json:"readOnly"`
	Problem  string         `json:"problem,omitempty"`
	Raw      string         `json:"raw,omitempty"`
	Reserved map[int]string `json:"reserved"`
	UIPort   int            `json:"uiPort"`
	Path     string         `json:"path"`
}

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	resp := devicesResp{Devices: []Device{}, Reserved: map[int]string{}, UIPort: s.uiPort, Path: s.p.ProxyConfig}
	data, err := os.ReadFile(s.p.ProxyConfig)
	if err != nil {
		resp.ReadOnly, resp.Problem = true, "Config not readable: "+err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Hash = hashBytes(data)
	cfg, err := parseProxyConfig(data)
	if err != nil {
		resp.ReadOnly, resp.Problem, resp.Raw = true, err.Error(), string(data)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Devices = cfg.Devices
	resp.Reserved = otherListeningPorts(cfg, s.uiPort)
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleSaveDevices(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Devices  []Device `json:"devices"`
		BaseHash string   `json:"baseHash"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	data, err := os.ReadFile(s.p.ProxyConfig)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Config not readable: "+err.Error())
		return
	}
	if hashBytes(data) != in.BaseHash {
		writeErr(w, http.StatusConflict, "The config has been changed in the meantime. Please reload the page.")
		return
	}
	cfg, err := parseProxyConfig(data)
	if err != nil {
		writeErr(w, http.StatusConflict, "The config cannot be parsed, saving is blocked: "+err.Error())
		return
	}
	devs, errs := normalizeAndValidate(in.Devices, otherListeningPorts(cfg, s.uiPort))
	if len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Please check the marked fields.", "errors": errs})
		return
	}
	if string(renderProxyConfig(cfg.Header, devs)) == string(data) {
		writeJSON(w, http.StatusOK, map[string]any{"unchanged": true})
		return
	}
	s.submit(w, Request{ID: newJobID(), Action: "apply", Devices: devs, BaseHash: in.BaseHash})
}

func (s *server) submit(w http.ResponseWriter, req Request) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if err := submitRequest(s.p, req); err != nil {
		if errors.Is(err, errBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "Request could not be queued: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": req.ID})
}

// --- jobs ---

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if st, err := readStatus(s.p); err == nil && id != "" && st.ID == id {
		writeJSON(w, http.StatusOK, st)
		return
	}
	if q, err := readQueued(s.p); err == nil && q.ID == id {
		writeJSON(w, http.StatusOK, JobStatus{ID: id, Action: q.Action, State: "queued",
			Message: "Waiting to be executed …", Started: q.Created})
		return
	}
	writeJSON(w, http.StatusOK, JobStatus{ID: id, State: "unknown", Message: "Job not found."})
}

func (s *server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	q, err := readQueued(s.p)
	if err != nil || q.ID != id {
		writeErr(w, http.StatusConflict, "The job is no longer queued.")
		return
	}
	if err := os.Remove(s.p.RequestFile()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// --- service ---

func (s *server) handleService(w http.ResponseWriter, r *http.Request) {
	var proxy, path ServiceStatus
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); proxy = serviceStatus(s.p.ProxyUnit) }()
	go func() { defer wg.Done(); path = serviceStatus(s.p.ApplyPath) }()
	wg.Wait()
	resp := map[string]any{"proxy": proxy, "applyPath": path}
	if st, err := readStatus(s.p); err == nil {
		resp["lastJob"] = st
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	switch in.Action {
	case "start", "stop", "restart":
	default:
		writeErr(w, http.StatusBadRequest, "Unknown action.")
		return
	}
	s.submit(w, Request{ID: newJobID(), Action: in.Action})
}

// --- connections ---

// Client = one connected host with all its connections (source ports).
type Client struct {
	IP    string `json:"ip"`
	Host  string `json:"host"`
	Ports []int  `json:"ports"`
}

type DeviceConn struct {
	Device
	Listening   bool     `json:"listening"`
	Clients     []Client `json:"clients"`
	Connections int      `json:"connections"`
	Upstream    int      `json:"upstream"` // open connections from the proxy to the device
}

type ListenInfo struct {
	Port  int    `json:"port"`
	Addr  string `json:"addr"`
	Label string `json:"label"`
}

func (s *server) handleConnections(w http.ResponseWriter, r *http.Request) {
	socks, err := readSockets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Sockets not readable: "+err.Error())
		return
	}
	var devs []Device
	if data, err := os.ReadFile(s.p.ProxyConfig); err == nil {
		if cfg, err := parseProxyConfig(data); err == nil {
			devs = cfg.Devices
		}
	}
	byPort := map[int]string{}
	out := make([]DeviceConn, 0, len(devs))
	allIPs := map[string]bool{}
	for _, d := range devs {
		byPort[d.ListenPort] = displayName(d.Name)
		dc := DeviceConn{Device: d, Clients: []Client{}}
		byIP := map[string]*Client{}
		for _, sk := range socks {
			switch {
			case sk.State == tcpListen && sk.LocalPort == d.ListenPort:
				dc.Listening = true
			case sk.State == tcpEstablished && sk.LocalPort == d.ListenPort:
				ip := sk.RemoteIP.String()
				c := byIP[ip]
				if c == nil {
					c = &Client{IP: ip}
					byIP[ip] = c
				}
				c.Ports = append(c.Ports, sk.RemotePort)
				dc.Connections++
				allIPs[ip] = true
			case sk.State == tcpEstablished && sk.RemotePort == d.Port && sk.RemoteIP.String() == d.Host:
				dc.Upstream++
			}
		}
		for _, c := range byIP {
			sort.Ints(c.Ports)
			dc.Clients = append(dc.Clients, *c)
		}
		sort.Slice(dc.Clients, func(i, j int) bool {
			a, b := net.ParseIP(dc.Clients[i].IP), net.ParseIP(dc.Clients[j].IP)
			return string(a.To16()) < string(b.To16())
		})
		out = append(out, dc)
	}
	// look up hostnames (cached)
	ips := make([]string, 0, len(allIPs))
	for ip := range allIPs {
		ips = append(ips, ip)
	}
	names := s.hosts.lookupAll(ips)
	for i := range out {
		for j := range out[i].Clients {
			out[i].Clients[j].Host = names[out[i].Clients[j].IP]
		}
	}
	seen := map[string]bool{}
	var listen []ListenInfo
	for _, sk := range socks {
		if sk.State != tcpListen {
			continue
		}
		key := fmt.Sprintf("%s|%d", sk.LocalIP, sk.LocalPort)
		if seen[key] {
			continue
		}
		seen[key] = true
		label := byPort[sk.LocalPort]
		if label != "" {
			label = "Proxy: " + label
		} else {
			label = portLabel(sk.LocalPort, s.uiPort)
		}
		listen = append(listen, ListenInfo{Port: sk.LocalPort, Addr: sk.LocalIP.String(), Label: label})
	}
	sort.Slice(listen, func(i, j int) bool {
		if listen[i].Port != listen[j].Port {
			return listen[i].Port < listen[j].Port
		}
		return listen[i].Addr < listen[j].Addr
	})
	writeJSON(w, http.StatusOK, map[string]any{"devices": out, "listening": listen, "time": time.Now().Unix()})
}

// --- Modbus test ---

func (s *server) handleModbus(w http.ResponseWriter, r *http.Request) {
	var q ModbusRequest
	if err := readJSON(w, r, &q); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request.")
		return
	}
	if err := q.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	allowed := false
	if data, err := os.ReadFile(s.p.ProxyConfig); err == nil {
		if cfg, err := parseProxyConfig(data); err == nil {
			for _, d := range cfg.Devices {
				if d.ListenPort == q.Port {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		writeErr(w, http.StatusBadRequest, "Only proxy ports from the config can be queried.")
		return
	}
	writeJSON(w, http.StatusOK, modbusQuery(q))
}

// --- System ---

func (s *server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemInfo(s.p))
}
