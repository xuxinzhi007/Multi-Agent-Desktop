// Package workdir 判断一个目录能不能当工作目录。
//
// macOS 对主目录下的桌面、文稿、下载各问一次权限，没有「全部允许」。
// 把这些目录本身设成工作目录时，shell 和代理会逐个碰到它们，弹窗就停不下来。
// 项目放在文稿里面的子目录是允许的：系统最多问一次「文稿」，问完就记住。
package workdir

import (
	"os"
	"path/filepath"
	"strings"
)

// RejectReason 是拒绝把某个目录当工作目录时给用户和模型看的原因。
const RejectReason = "工作目录不能是主目录、桌面、文稿或下载。请改成一个项目文件夹，这样系统不会按这些位置反复询问权限。"

// TooBroad 报告 dir 会覆盖整台机器、整个主目录，或某一个受系统单独保护的文件夹。
func TooBroad(dir string) bool {
	if dir == "" || dir == "." || dir == "/" {
		return true
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) || volumeRoot(abs) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	home = filepath.Clean(home)
	if abs == home {
		return true
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads", "Pictures", "Movies", "Music"} {
		if abs == filepath.Join(home, name) {
			return true
		}
	}
	return false
}

// volumeRoot 报告 abs 是不是盘符根，例如 C:\。Unix 上 VolumeName 为空。
func volumeRoot(abs string) bool {
	vol := filepath.VolumeName(abs)
	if vol == "" {
		return false
	}
	rest := strings.TrimPrefix(abs, vol)
	rest = strings.Trim(rest, `/\`)
	return rest == ""
}
