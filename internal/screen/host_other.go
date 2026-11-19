//go:build !darwin

package screen

import "errors"

func FrontApp() (string, error) { return "", errHost }

func ShotDisplay(string) error { return errHost }

func ReadText(string) (string, error) { return "", errHost }

func NeedsBuild() bool { return false }

func OwnApp(name string) bool { return false }

var errHost = errors.New("屏幕协助目前只在 macOS 上可用")
