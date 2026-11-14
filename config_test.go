package main

import (
	"strings"
	"testing"
)

// Example config in exactly the format written by the UI.
const sampleConfig = `devices:
# Energy meter (ID 1) + heat pump meter (ID 2), RS485 gateway in Modbus TCP mode
- modbus:
    url: 192.168.1.101:502
    timeout: 1
  listen:
    bind: 0:50201

# Inverter, RS485 gateway in Modbus TCP mode
- modbus:
    url: 192.168.1.109:502
    timeout: 3
  listen:
    bind: 0:50209

# Wallbox (ID 255) – Ümlaut-Test
- modbus:
    url: 192.168.1.111:502
    timeout: 5
  listen:
    bind: 0:50211
`

func TestRoundTrip(t *testing.T) {
	cfg, err := parseProxyConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(cfg.Devices))
	}
	d := cfg.Devices[0]
	if d.Name != "Energy meter (ID 1) + heat pump meter (ID 2), RS485 gateway in Modbus TCP mode" ||
		d.Host != "192.168.1.101" || d.Port != 502 || d.Timeout != 1 || d.ListenPort != 50201 {
		t.Fatalf("device 1 wrong: %+v", d)
	}
	out := renderProxyConfig(cfg.Header, cfg.Devices)
	if string(out) != sampleConfig {
		t.Fatalf("round trip changes the file:\n%s", out)
	}
	devs, errs := normalizeAndValidate(cfg.Devices, map[int]string{22: "SSH", 80: "Web UI"})
	if len(errs) > 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if string(renderProxyConfig(cfg.Header, devs)) != sampleConfig {
		t.Fatal("normalization changes the file")
	}
}

func TestHeaderBOMAndUnknown(t *testing.T) {
	in := "# Header\ndevices:\n# A\n- modbus:\n    url: 10.0.0.1:502\n    timeout: 2\n  listen:\n    bind: 0:50201\n"
	cfg, err := parseProxyConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(renderProxyConfig(cfg.Header, cfg.Devices)) != in {
		t.Fatal("header comment is lost")
	}
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	if _, err := parseProxyConfig([]byte(bom + in)); err != nil {
		t.Fatalf("UTF-8 BOM must be accepted: %v", err)
	}
	bad := strings.Replace(in, "    timeout: 2\n", "    timeout: 2\n    foo: 1\n", 1)
	if _, err := parseProxyConfig([]byte(bad)); err == nil {
		t.Fatal("unknown setting must be rejected")
	}
	if _, err := parseProxyConfig([]byte(strings.Replace(in, "timeout: 2", "timeout: 1.5", 1))); err == nil {
		t.Fatal("fractional timeout must be rejected")
	}
}

func TestValidate(t *testing.T) {
	devs := []Device{
		{Name: "A", Host: "192.168.1.101", Port: 502, Timeout: 1, ListenPort: 50201},
		{Name: "B", Host: "192.168.1.201", Port: 502, Timeout: 1, ListenPort: 50201},
		{Name: "", Host: "300.1.1.1", Port: 0, Timeout: 0, ListenPort: 80},
		{Name: "C", Host: "192.168.1.101", Port: 502, Timeout: 99, ListenPort: 22222},
	}
	_, errs := normalizeAndValidate(devs, map[int]string{22222: "other program"})
	want := map[string]bool{
		"1/listenPort": true, // duplicate
		"2/name":       true, "2/host": true, "2/port": true, "2/timeout": true, "2/listenPort": true,
		"3/host": true, "3/timeout": true, "3/listenPort": true, // duplicate target, timeout, port in use
	}
	got := map[string]bool{}
	for _, e := range errs {
		got[strings.Join([]string{string(rune('0' + e.Index)), e.Field}, "/")] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("expected error %s, got: %v", k, errs)
		}
	}
	if len(got) != len(want) {
		t.Errorf("too many errors: %v", errs)
	}
}

func TestHexAddr(t *testing.T) {
	ip, port, ok := parseHexAddr("0100007F:C3A1")
	if !ok || ip.String() != "127.0.0.1" || port != 50081 {
		t.Fatalf("%v %v %v", ip, port, ok)
	}
	ip, port, ok = parseHexAddr("0000000000000000FFFF00003901A8C0:01F6")
	if !ok || ip.String() != "192.168.1.57" || port != 502 {
		t.Fatalf("v4-mapped: %v %v %v", ip, port, ok)
	}
}
