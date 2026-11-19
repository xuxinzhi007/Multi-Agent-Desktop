//go:build darwin

package privacy

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed grant.swift
var grantFS embed.FS

var once sync.Mutex

func Status() map[string]string {
	out := map[string]string{
		"screen":        "unknown",
		"accessibility": "unknown",
		"automation":    "unknown",
	}
	bin, err := binary()
	if err != nil {
		return out
	}
	raw, err := exec.Command(bin).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "yes", "no", "unknown":
			out[fields[0]] = fields[1]
		}
	}
	return out
}

func binary() (string, error) {
	once.Lock()
	defer once.Unlock()
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "multi-agent-desktop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := grantFS.ReadFile("grant.swift")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(src)
	hash := hex.EncodeToString(sum[:])
	bin := filepath.Join(dir, "privacy-grant")
	stamp := filepath.Join(dir, "privacy-grant.sha")
	if prev, err := os.ReadFile(stamp); err == nil && string(prev) == hash {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	srcPath := filepath.Join(dir, "grant.swift")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("xcrun", "swiftc", "-O", "-o", bin, srcPath)
	compiled, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	_ = compiled
	if err := os.WriteFile(stamp, []byte(hash), 0o644); err != nil {
		return "", err
	}
	return bin, nil
}
