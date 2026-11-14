package main

// Login: password hash (PBKDF2-SHA256 from the Go standard library),
// sessions in memory only, throttling against brute force.

import (
	"bufio"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pbkdfIter      = 600000
	sessionIdle    = 12 * time.Hour
	maxSessions    = 100
	failWindow     = time.Minute
	maxFailsWindow = 5
	cookieName     = "modbusui_session"
)

var b64 = base64.RawStdEncoding

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, b64.EncodeToString(salt), b64.EncodeToString(k)), nil
}

var errNoPassword = errors.New("no password set")

func checkPassword(file, pw string) (bool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return false, errNoPassword
		}
		return false, err
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, errors.New("password file has an unknown format")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 10000000 {
		return false, errors.New("password file: invalid iteration count")
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false, errors.New("password file is corrupt")
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type sessionStore struct {
	mu    sync.Mutex
	m     map[string]time.Time // token -> expiry
	fails []time.Time
}

func newSessionStore() *sessionStore {
	s := &sessionStore{m: map[string]time.Time{}}
	go func() {
		for range time.Tick(10 * time.Minute) {
			s.mu.Lock()
			now := time.Now()
			for k, exp := range s.m {
				if now.After(exp) {
					delete(s.m, k)
				}
			}
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= maxSessions {
		var oldestK string
		var oldest time.Time
		for k, exp := range s.m {
			if oldestK == "" || exp.Before(oldest) {
				oldestK, oldest = k, exp
			}
		}
		delete(s.m, oldestK)
	}
	s.m[tok] = time.Now().Add(sessionIdle)
	return tok
}

// valid checks a token and extends the session.
func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	if !ok || time.Now().After(exp) {
		delete(s.m, tok)
		return false
	}
	s.m[tok] = time.Now().Add(sessionIdle)
	return true
}

func (s *sessionStore) remove(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

func (s *sessionStore) pruneFails() {
	cut := time.Now().Add(-failWindow)
	i := 0
	for i < len(s.fails) && s.fails[i].Before(cut) {
		i++
	}
	s.fails = s.fails[i:]
}

func (s *sessionStore) tooManyFails() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneFails()
	return len(s.fails) >= maxFailsWindow
}

func (s *sessionStore) addFail() {
	s.mu.Lock()
	s.pruneFails()
	s.fails = append(s.fails, time.Now())
	s.mu.Unlock()
}

// --- modbusui -set-password ---

func readSecret(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	echoOff := exec.Command("stty", "-echo")
	echoOff.Stdin = os.Stdin
	hidden := echoOff.Run() == nil
	line, err := r.ReadString('\n')
	if hidden {
		echoOn := exec.Command("stty", "echo")
		echoOn.Stdin = os.Stdin
		_ = echoOn.Run()
		fmt.Fprintln(os.Stderr)
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runSetPassword(p Paths) int {
	r := bufio.NewReader(os.Stdin)
	pw1, err := readSecret(r, "New password for the web UI: ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Aborted:", err)
		return 1
	}
	if len([]rune(pw1)) < 8 {
		fmt.Fprintln(os.Stderr, "The password must have at least 8 characters.")
		return 1
	}
	pw2, err := readSecret(r, "Repeat password: ")
	if err != nil || pw1 != pw2 {
		fmt.Fprintln(os.Stderr, "The passwords do not match.")
		return 1
	}
	h, err := hashPassword(pw1)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}

	gid := -1
	if g, err := user.LookupGroup("modbusui"); err == nil {
		gid, _ = strconv.Atoi(g.Gid)
	} else {
		fmt.Fprintln(os.Stderr, "Note: group modbusui not found, the web UI will not be able to read the file.")
	}
	dir := filepath.Dir(p.PasswordFile)
	if err := os.MkdirAll(dir, 0750); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	tmp := p.PasswordFile + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte(h+"\n"), 0640); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	_ = os.Chmod(tmp, 0640)
	if gid >= 0 {
		_ = os.Chown(dir, 0, gid)
		_ = os.Chmod(dir, 0750)
		_ = os.Chown(tmp, 0, gid)
	}
	if err := os.Rename(tmp, p.PasswordFile); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Password saved to", p.PasswordFile)
	return 0
}
