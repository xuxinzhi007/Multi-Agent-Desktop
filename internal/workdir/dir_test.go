package workdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTooBroad(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "Documents", "gowork", "app")
	cases := []struct {
		dir  string
		want bool
	}{
		{"", true},
		{".", true},
		{"/", true},
		{home, true},
		{filepath.Join(home, "Desktop"), true},
		{filepath.Join(home, "Documents"), true},
		{filepath.Join(home, "Downloads"), true},
		{project, false},
		{t.TempDir(), false},
	}
	for _, tc := range cases {
		if got := TooBroad(tc.dir); got != tc.want {
			t.Errorf("TooBroad(%q) = %v, want %v", tc.dir, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		root := filepath.VolumeName(home) + `\`
		if !TooBroad(root) {
			t.Errorf("TooBroad(%q) = false, want true", root)
		}
		child := filepath.Join(root, "Windows")
		if TooBroad(child) {
			t.Errorf("TooBroad(%q) = true, want false", child)
		}
	}
}
