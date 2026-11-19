package screen

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// Fingerprint 把一帧缩成 32×32 的粗亮度，用来判断画面有没有停稳。
// 不哈希原始文件：截屏工具会在每张图里写时间，同一画面也会得到不同字节。
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	if b.Dx() < 2 || b.Dy() < 2 {
		return "", os.ErrInvalid
	}
	var buf [32 * 32]byte
	for y := 0; y < 32; y++ {
		sy := b.Min.Y + (y*b.Dy())/32
		for x := 0; x < 32; x++ {
			sx := b.Min.X + (x*b.Dx())/32
			r, g, bl, _ := img.At(sx, sy).RGBA()
			lum := (r + g + bl) / 3
			buf[y*32+x] = byte(lum >> 12)
		}
	}
	sum := sha256.Sum256(buf[:])
	return hex.EncodeToString(sum[:8]), nil
}
