package privacy

import (
	"runtime"
	"testing"
)

func TestStatusKeys(t *testing.T) {
	got := Status()
	for _, key := range []string{"screen", "accessibility", "automation"} {
		switch got[key] {
		case "yes", "no", "unknown":
		default:
			t.Fatalf("%s = %q", key, got[key])
		}
	}
	if runtime.GOOS == "darwin" && got["screen"] == "unknown" && got["accessibility"] == "unknown" {
		t.Fatalf("darwin 上没读到权限状态: %+v", got)
	}
}
