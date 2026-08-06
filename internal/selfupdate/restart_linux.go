//go:build linux

package selfupdate

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process image with the (just-updated) binary
// using the original arguments. exec keeps the systemd unit's PID tracking
// intact, so it works under Restart=on-failure without a special exit code.
func Restart() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位当前可执行文件失败: %w", err)
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
