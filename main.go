// modbusui – web UI for modbus-proxy-rs.
//
// One binary, three modes:
//
//	modbusui                 web UI (runs as user modbusui, via modbusui.service)
//	modbusui -apply          executes a request from the spool (root, via modbus-proxy-apply.service)
//	modbusui -set-password   sets the login password (root, by hand)
//
// In normal operation the program writes nothing to stdout/stderr.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

// version can be set at build time: -ldflags "-X main.version=1.2.3"
var version = "1.0.0"

// Paths bundles all paths. They can be changed by flags so that a test
// instance can run next to the real proxy.
type Paths struct {
	ProxyConfig  string
	ProxyUnit    string
	ApplyPath    string
	StateDir     string
	PasswordFile string
}

func (p Paths) SpoolDir() string    { return filepath.Join(p.StateDir, "spool") }
func (p Paths) RequestFile() string { return filepath.Join(p.SpoolDir(), "request.json") }
func (p Paths) ResultDir() string   { return filepath.Join(p.StateDir, "result") }
func (p Paths) StatusFile() string  { return filepath.Join(p.ResultDir(), "status.json") }
func (p Paths) BackupDir() string   { return filepath.Join(p.StateDir, "backup") }

var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]*\.(service|path)$`)

func main() {
	var p Paths
	listen := flag.String("listen", ":80", "address of the web UI")
	apply := flag.Bool("apply", false, "execute the request in the spool (root, called by systemd)")
	setPw := flag.Bool("set-password", false, "set the login password (run as root)")
	showVersion := flag.Bool("version", false, "print version")
	flag.StringVar(&p.ProxyConfig, "proxy-config", "/etc/modbus-proxy.yml", "proxy config file")
	flag.StringVar(&p.ProxyUnit, "proxy-unit", "modbus-proxy.service", "systemd unit of the proxy")
	flag.StringVar(&p.ApplyPath, "apply-path-unit", "modbus-proxy-apply.path", "systemd path unit that picks up requests")
	flag.StringVar(&p.StateDir, "state-dir", "/var/lib/modbusui", "working directory (spool, result, backup)")
	flag.StringVar(&p.PasswordFile, "password-file", "/etc/modbusui/password", "file with the password hash")
	flag.Parse()

	// Nothing to the journal: silence the standard logger.
	log.SetOutput(io.Discard)

	if !unitNameRe.MatchString(p.ProxyUnit) || !unitNameRe.MatchString(p.ApplyPath) {
		fmt.Fprintln(os.Stderr, "modbusui: invalid unit name")
		os.Exit(2)
	}

	switch {
	case *showVersion:
		fmt.Println("modbusui", version)
	case *setPw:
		os.Exit(runSetPassword(p))
	case *apply:
		os.Exit(runApply(p))
	default:
		os.Exit(runServer(p, *listen))
	}
}
