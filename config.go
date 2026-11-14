package main

// Reading, validating and writing the proxy config /etc/modbus-proxy.yml.
//
// Deliberately not a general YAML parser: exactly the format the UI writes is
// understood. If anything unknown shows up, saving is blocked so that nothing
// gets lost when the file is written back.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxDevices = 50

type Device struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Timeout    int    `json:"timeout"`
	ListenPort int    `json:"listenPort"`
}

type ProxyConfig struct {
	Header  []string // comment lines before "devices:"
	Devices []Device
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func displayName(n string) string {
	if n == "" {
		return "unnamed"
	}
	return n
}

func unquote(v string) string {
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}

func parseProxyConfig(data []byte) (*ProxyConfig, error) {
	cfg := &ProxyConfig{}
	var pending []string
	var cur *Device
	have := map[string]bool{}
	section := ""
	inDevices := false

	finish := func() error {
		if cur == nil {
			return nil
		}
		for _, k := range []string{"url", "timeout", "bind"} {
			if !have[k] {
				return fmt.Errorf("entry %d (%s): %q is missing", len(cfg.Devices)+1, displayName(cur.Name), k)
			}
		}
		cfg.Devices = append(cfg.Devices, *cur)
		cur = nil
		return nil
	}

	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), string(rune(0xFEFF)))
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		line := strings.TrimRight(raw, " \t")
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			if !inDevices {
				cfg.Header = append(cfg.Header, line)
				continue
			}
			if err := finish(); err != nil {
				return nil, err
			}
			if c := strings.TrimSpace(strings.TrimPrefix(t, "#")); c != "" {
				pending = append(pending, c)
			}
			continue
		}
		if !inDevices {
			if line == "devices:" {
				inDevices = true
				continue
			}
			return nil, fmt.Errorf("line %d: unexpected %q, expected \"devices:\"", n, t)
		}
		if strings.HasPrefix(t, "-") {
			if strings.TrimSpace(t[1:]) != "modbus:" {
				return nil, fmt.Errorf("line %d: %q is not supported (expected \"- modbus:\")", n, t)
			}
			if err := finish(); err != nil {
				return nil, err
			}
			cur = &Device{Name: strings.Join(pending, " – ")}
			pending = nil
			have = map[string]bool{}
			section = "modbus"
			continue
		}
		key, val, ok := strings.Cut(t, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: cannot parse %q", n, t)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if j := strings.Index(val, " #"); j >= 0 {
			val = strings.TrimSpace(val[:j])
		}
		val = unquote(val)
		if cur == nil {
			return nil, fmt.Errorf("line %d: %q is outside of a device entry", n, t)
		}
		if have[key] {
			return nil, fmt.Errorf("line %d: %q appears twice in the same entry", n, key)
		}
		switch {
		case key == "listen" && val == "":
			section = "listen"
		case section == "modbus" && key == "url":
			h, ps, err := net.SplitHostPort(val)
			if err != nil {
				return nil, fmt.Errorf("line %d: url %q is not in the form IP:port", n, val)
			}
			port, err := strconv.Atoi(ps)
			if err != nil {
				return nil, fmt.Errorf("line %d: port in url %q is not a number", n, val)
			}
			cur.Host, cur.Port = h, port
		case section == "modbus" && key == "timeout":
			to, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("line %d: timeout %q is not a whole number (not supported by the UI)", n, val)
			}
			cur.Timeout = to
		case section == "listen" && key == "bind":
			h, ps, err := net.SplitHostPort(val)
			if err != nil {
				return nil, fmt.Errorf("line %d: bind %q is not in the form address:port", n, val)
			}
			if h != "0" && h != "0.0.0.0" && h != "" {
				return nil, fmt.Errorf("line %d: bind address %q is not supported by the UI (only 0)", n, h)
			}
			port, err := strconv.Atoi(ps)
			if err != nil {
				return nil, fmt.Errorf("line %d: port in bind %q is not a number", n, val)
			}
			cur.ListenPort = port
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q", n, t)
		}
		have[key] = true
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if !inDevices {
		return nil, fmt.Errorf("\"devices:\" is missing")
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("comment at the end of the file does not belong to a device (%q)", strings.Join(pending, " "))
	}
	return cfg, nil
}

func renderProxyConfig(header []string, devs []Device) []byte {
	var b strings.Builder
	for _, h := range header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	b.WriteString("devices:\n")
	for i, d := range devs {
		if i > 0 {
			b.WriteByte('\n')
		}
		if d.Name != "" {
			fmt.Fprintf(&b, "# %s\n", d.Name)
		}
		fmt.Fprintf(&b, "- modbus:\n    url: %s:%d\n    timeout: %d\n  listen:\n    bind: 0:%d\n",
			d.Host, d.Port, d.Timeout, d.ListenPort)
	}
	return []byte(b.String())
}

func listenPorts(devs []Device) []int {
	ps := make([]int, 0, len(devs))
	for _, d := range devs {
		ps = append(ps, d.ListenPort)
	}
	return ps
}

// FieldError describes a validation error. Index -1 = concerns the whole list.
type FieldError struct {
	Index int    `json:"index"`
	Field string `json:"field"`
	Msg   string `json:"msg"`
}

// normalizeAndValidate checks the device list. reserved holds ports that are
// in use by other programs (port -> description).
func normalizeAndValidate(devs []Device, reserved map[int]string) ([]Device, []FieldError) {
	var errs []FieldError
	add := func(i int, f, format string, a ...any) {
		errs = append(errs, FieldError{Index: i, Field: f, Msg: fmt.Sprintf(format, a...)})
	}
	if len(devs) == 0 {
		add(-1, "", "At least one device is required.")
	}
	if len(devs) > maxDevices {
		add(-1, "", "At most %d devices.", maxDevices)
		return nil, errs
	}
	out := make([]Device, len(devs))
	listenSeen := map[int]int{}
	targetSeen := map[string]int{}
	for i, d := range devs {
		d.Name = strings.TrimSpace(d.Name)
		switch {
		case d.Name == "":
			add(i, "name", "Name is missing.")
		case !utf8.ValidString(d.Name) || strings.IndexFunc(d.Name, unicode.IsControl) >= 0:
			add(i, "name", "Name contains invalid characters.")
		case utf8.RuneCountInString(d.Name) > 100:
			add(i, "name", "Name is too long (max. 100 characters).")
		}

		d.Host = strings.TrimSpace(d.Host)
		ip := net.ParseIP(d.Host)
		hostOK := false
		if ip == nil || ip.To4() == nil {
			add(i, "host", "Not a valid IPv4 address.")
		} else {
			ip4 := ip.To4()
			if ip4.IsUnspecified() || ip4.IsLoopback() || ip4.IsMulticast() || ip4.Equal(net.IPv4bcast) {
				add(i, "host", "This address cannot be used for a device.")
			} else {
				d.Host = ip4.String()
				hostOK = true
			}
		}

		if d.Port < 1 || d.Port > 65535 {
			add(i, "port", "Device port must be between 1 and 65535.")
		} else if hostOK {
			t := fmt.Sprintf("%s:%d", d.Host, d.Port)
			if j, dup := targetSeen[t]; dup {
				add(i, "host", "Target %s is already used by device %d (%s).", t, j+1, displayName(devs[j].Name))
			} else {
				targetSeen[t] = i
			}
		}

		if d.Timeout < 1 || d.Timeout > 60 {
			add(i, "timeout", "Timeout must be between 1 and 60 seconds.")
		}

		switch {
		case d.ListenPort < 1024 || d.ListenPort > 65535:
			add(i, "listenPort", "Proxy port must be between 1024 and 65535.")
		default:
			if j, dup := listenSeen[d.ListenPort]; dup {
				add(i, "listenPort", "Proxy port %d is already used by device %d (%s).", d.ListenPort, j+1, displayName(devs[j].Name))
			} else if who, busy := reserved[d.ListenPort]; busy {
				add(i, "listenPort", "Port %d is already in use (%s).", d.ListenPort, who)
			} else {
				listenSeen[d.ListenPort] = i
			}
		}
		out[i] = d
	}
	return out, errs
}

func joinFieldErrors(errs []FieldError) string {
	var parts []string
	for _, e := range errs {
		if e.Index >= 0 {
			parts = append(parts, fmt.Sprintf("Device %d: %s", e.Index+1, e.Msg))
		} else {
			parts = append(parts, e.Msg)
		}
	}
	return strings.Join(parts, " ")
}
