//go:build darwin

package screen

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed vision.swift
var visionFS embed.FS

func FrontApp() (string, error) {
	out, err := exec.Command("osascript", "-e", `tell application "System Events" to get name of first application process whose frontmost is true`).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func ShotDisplay(path string) error {
	if id := frontWindow(); id > 0 {
		if err := capture(path, "-l", strconv.Itoa(id)); err == nil {
			return nil
		}
	}
	return capture(path, "-D", "1")
}

func capture(path, flag, value string) error {
	cmd := exec.Command("/usr/sbin/screencapture", "-x", "-t", "jpg", flag, value, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("截屏失败（需要在系统设置里允许屏幕录制）：%s", msg)
	}
	st, statErr := os.Stat(path)
	if statErr != nil || st.Size() < 32 {
		return errors.New("截屏是空的，先在系统设置 → 隐私与安全性 → 屏幕录制里允许本应用")
	}
	return nil
}

func frontWindow() int {
	bin, err := ocrBinary()
	if err != nil {
		return 0
	}
	own := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	out, err := exec.Command(bin, "--front-window", own).Output()
	if err != nil {
		return 0
	}
	id, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return id
}

func ReadText(path string) (string, error) {
	bin, err := ocrBinary()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("读屏失败：%s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func NeedsBuild() bool {
	_, err := ocrBinaryPath()
	return err != nil
}

func OwnApp(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if strings.Contains(n, "multi-agent") || strings.Contains(n, "agent desktop") || strings.Contains(n, "舰队") {
		return true
	}
	base := strings.ToLower(filepath.Base(os.Args[0]))
	base = strings.TrimSuffix(base, ".exe")
	return base != "" && (n == base || strings.Contains(n, base))
}

var ocrOnce sync.Mutex

func ocrBinary() (string, error) {
	ocrOnce.Lock()
	defer ocrOnce.Unlock()
	if bin, err := ocrBinaryPath(); err == nil {
		return bin, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "multi-agent-desktop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := visionFS.ReadFile("vision.swift")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(src)
	hash := hex.EncodeToString(sum[:])
	srcPath := filepath.Join(dir, "vision.swift")
	bin := filepath.Join(dir, "vision-ocr")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("xcrun", "swiftc", "-O", "-o", bin, srcPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("准备读屏失败（需要 Xcode 命令行工具）：%s", msg)
	}
	if err := os.WriteFile(filepath.Join(dir, "vision.swift.sha"), []byte(hash), 0o644); err != nil {
		return "", err
	}
	return bin, nil
}

func ocrBinaryPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "multi-agent-desktop")
	src, err := visionFS.ReadFile("vision.swift")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(src)
	want := hex.EncodeToString(sum[:])
	got, err := os.ReadFile(filepath.Join(dir, "vision.swift.sha"))
	if err != nil || strings.TrimSpace(string(got)) != want {
		return "", os.ErrNotExist
	}
	bin := filepath.Join(dir, "vision-ocr")
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return "", os.ErrNotExist
	}
	return bin, nil
}
