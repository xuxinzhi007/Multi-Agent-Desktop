//go:build !darwin

package browser

import (
	"context"
	"errors"
)

func Act(context.Context, Op) (string, error) {
	return "", errors.New("操作网页目前只在 macOS 上可用")
}
