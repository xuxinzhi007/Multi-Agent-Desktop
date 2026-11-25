package shellguard

import (
	"os"
	"testing"
)

func TestBlocksDestructiveRootCommands(t *testing.T) {
	blocked := []string{
		"rm -rf /",
		"sudo rm -rf /*",
		"rm -rf ~",
		"Remove-Item -Recurse -Force C:\\",
		"del /s /q C:\\",
		"format c:",
		"Clear-Disk -Number 1",
		"shutdown /s /t 0",
		"dd if=/dev/zero of=/dev/sda",
		":(){ :|:& };:",
	}
	for _, cmd := range blocked {
		if ok, _ := Check(cmd); !ok {
			t.Errorf("should block: %s", cmd)
		}
	}
}

func TestAllowsNormalDevelopmentCommands(t *testing.T) {
	ok := []string{
		"rm -rf node_modules",
		"git clean -fd",
		"go test ./...",
		"Remove-Item -Recurse -Force .\\dist",
	}
	for _, cmd := range ok {
		if blocked, reason := Check(cmd); blocked {
			t.Errorf("should allow %q, blocked: %s", cmd, reason)
		}
	}
}

func TestMultiLineWhitespaceCannotBypass(t *testing.T) {
	cmd := "rm   -rf \n /"
	if blocked, _ := Check(cmd); !blocked {
		t.Fatalf("whitespace/newline should still be caught: %q", cmd)
	}
}

func TestBlocksExpandedHomeAndSiblings(t *testing.T) {
	blocked := []string{
		"rm -rf $HOME",
		"rm -rf ${HOME}",
		"rm -rf ~",
		"rm -rf ~/",
		"mv ~ /tmp/x",
		"chmod -R 000 ~",
		"cd /tmp && rm -rf ~",
		"find / -delete",
		"rm -rf /etc",
		"rm -rf /usr",
	}
	for _, cmd := range blocked {
		if ok, _ := Check(cmd); !ok {
			t.Errorf("should block: %s", cmd)
		}
	}
}

func TestAllowsWorkInsideHomeAndProject(t *testing.T) {
	ok := []string{
		"rm -rf node_modules",
		"rm -rf ./dist",
		"rm -rf build/cache",
		"git clean -fd",
		"go build ./...",
		"npm install",
		"chmod +x ./scripts/run.sh",
		"mv src/a.ts src/b.ts",
		"find . -name '*.log' -delete",
	}
	for _, cmd := range ok {
		if blocked, reason := Check(cmd); blocked {
			t.Errorf("should allow %q, blocked: %s", cmd, reason)
		}
	}
}

func TestAllowsHomeSubdirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	// 家目录下面的具体项目目录是正常工作区，不能拦。
	for _, cmd := range []string{
		"rm -rf " + home + "/Documents/gowork/tmp",
		"rm -rf ~/Documents/gowork/tmp",
	} {
		if blocked, reason := Check(cmd); blocked {
			t.Errorf("should allow %q, blocked: %s", cmd, reason)
		}
	}
}
