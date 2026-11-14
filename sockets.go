package main

// Read TCP sockets from /proc/net/tcp and /proc/net/tcp6
// (the same data as "ss -tn"), without external programs.

import (
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	tcpEstablished = 0x01
	tcpListen      = 0x0A
)

type Sock struct {
	LocalIP    net.IP
	LocalPort  int
	RemoteIP   net.IP
	RemotePort int
	State      int
}

func readSockets() ([]Sock, error) {
	var all []Sock
	var firstErr error
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		s, err := parseProcNet(f)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, s...)
	}
	if all == nil && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

func parseProcNet(path string) ([]Sock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var out []Sock
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		lip, lport, ok1 := parseHexAddr(f[1])
		rip, rport, ok2 := parseHexAddr(f[2])
		st, err := strconv.ParseUint(f[3], 16, 8)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		out = append(out, Sock{LocalIP: lip, LocalPort: lport, RemoteIP: rip, RemotePort: rport, State: int(st)})
	}
	return out, nil
}

// parseHexAddr turns "0100007F:C3A1" into 127.0.0.1 and 50081.
// The kernel writes the address as 32-bit words in host byte order.
func parseHexAddr(s string) (net.IP, int, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, false
	}
	b, err := hex.DecodeString(h)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil, 0, false
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		ip[i], ip[i+1], ip[i+2], ip[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip, int(port), true
}

func listeningPorts(socks []Sock) map[int]bool {
	m := map[int]bool{}
	for _, s := range socks {
		if s.State == tcpListen {
			m[s.LocalPort] = true
		}
	}
	return m
}

var knownPorts = map[int]string{22: "SSH", 25: "mail server (local only)"}

func portLabel(port, uiPort int) string {
	if port == uiPort {
		return "Web UI"
	}
	if l, ok := knownPorts[port]; ok {
		return l
	}
	return "other program"
}

// otherListeningPorts returns all listening ports that do not belong to the
// proxy (= are not in the current config).
func otherListeningPorts(cfg *ProxyConfig, uiPort int) map[int]string {
	res := map[int]string{}
	own := map[int]bool{}
	if cfg != nil {
		for _, d := range cfg.Devices {
			own[d.ListenPort] = true
		}
	}
	socks, err := readSockets()
	if err != nil {
		return res
	}
	for p := range listeningPorts(socks) {
		if !own[p] {
			res[p] = portLabel(p, uiPort)
		}
	}
	if uiPort > 0 {
		res[uiPort] = "Web UI"
	}
	return res
}

func missingPorts(ports []int) []int {
	socks, err := readSockets()
	if err != nil {
		return ports
	}
	l := listeningPorts(socks)
	var miss []int
	for _, p := range ports {
		if !l[p] {
			miss = append(miss, p)
		}
	}
	return miss
}
