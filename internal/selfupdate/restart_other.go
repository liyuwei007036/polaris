//go:build !linux

package selfupdate

import "errors"

func Restart() error {
	return errors.New("自动重启仅支持 Linux；请手动重启 sb-control 服务")
}
