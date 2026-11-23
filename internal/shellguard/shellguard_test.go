package shellguard

import "testing"

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
