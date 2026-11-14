// release builds the release archives for all supported architectures.
//
//	go run ./tools/release            version from main.go
//	go run ./tools/release 1.2.0      explicit version
//
// Result in dist/ (see README, "Building it yourself"). Packing is done here in
// Go instead of with the system's tar, so that the archives are identical on
// Linux, macOS and Windows: owner root, directories 755, files 644, programs
// and scripts 755.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type target struct{ name, goarch, goarm string }

var targets = []target{
	{"amd64", "amd64", ""},
	{"arm64", "arm64", ""},
	{"armv7", "arm", "7"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func goBin() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return "go"
}

func sh(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

func run() error {
	version := ""
	if len(os.Args) > 1 {
		version = os.Args[1]
	} else {
		src, err := os.ReadFile("main.go")
		if err != nil {
			return fmt.Errorf("run this from the project folder: %v", err)
		}
		m := regexp.MustCompile(`(?m)^var version = "(.*)"`).FindSubmatch(src)
		if m == nil {
			return fmt.Errorf("version not found in main.go")
		}
		version = string(m[1])
	}
	if !regexp.MustCompile(`^[0-9A-Za-z._-]+$`).MatchString(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	gob := goBin()
	fmt.Println("modbusui", version)

	offline := []string{"GOPROXY=off"}
	if err := sh(offline, gob, "vet", "./..."); err != nil {
		return fmt.Errorf("go vet failed")
	}
	if err := sh(offline, gob, "test", "-count=1", "./..."); err != nil {
		return fmt.Errorf("go test failed")
	}

	if err := os.RemoveAll("dist"); err != nil {
		return err
	}
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	var outputs []string
	for _, t := range targets {
		bin := filepath.Join("dist", "modbusui-linux-"+t.name)
		fmt.Println("building linux/" + t.name)
		env := append(offline, "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+t.goarch, "GOARM="+t.goarm)
		if err := sh(env, gob, "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", bin, "."); err != nil {
			return fmt.Errorf("build failed for %s", t.name)
		}
		name := fmt.Sprintf("modbusui-%s-linux-%s", version, t.name)
		arch := filepath.Join("dist", name+".tar.gz")
		if err := pack(arch, name, bin); err != nil {
			return fmt.Errorf("packing %s: %v", t.name, err)
		}
		outputs = append(outputs, arch, bin)
	}
	sort.Strings(outputs)
	var sums strings.Builder
	for _, f := range outputs {
		h, err := sha256File(f)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", h, filepath.Base(f))
	}
	if err := os.WriteFile(filepath.Join("dist", "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	fmt.Println()
	for _, f := range outputs {
		fi, _ := os.Stat(f)
		fmt.Printf("%10d  %s\n", fi.Size(), f)
	}
	fmt.Println("           dist/SHA256SUMS")
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pack writes <name>/{modbusui, install.sh, README.md, LICENSE, deploy/...} into a .tar.gz.
func pack(archive, name, bin string) error {
	out, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	now := time.Now().Truncate(time.Second)

	addDir := func(p string) error {
		return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: p + "/", Mode: 0755,
			Uname: "root", Gname: "root", ModTime: now, Format: tar.FormatPAX})
	}
	addFile := func(src, dst string, mode int64) error {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: dst, Mode: mode, Size: int64(len(data)),
			Uname: "root", Gname: "root", ModTime: now, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	}

	if err := addDir(name); err != nil {
		return err
	}
	if err := addFile(bin, name+"/modbusui", 0755); err != nil {
		return err
	}
	if err := addFile("install.sh", name+"/install.sh", 0755); err != nil {
		return err
	}
	for _, f := range []string{"README.md", "LICENSE"} {
		if err := addFile(f, name+"/"+f, 0644); err != nil {
			return err
		}
	}
	err = filepath.WalkDir("deploy", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(p)
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return addDir(name + "/" + rel)
		}
		mode := int64(0644)
		if strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".py") {
			mode = 0755
		}
		return addFile(p, name+"/"+rel, mode)
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}
