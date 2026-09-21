//go:build unix

package update

import (
	"os"
	"syscall"
)

// Restart 原地重启：同一 PID 换新镜像（systemd 下服务不掉线，手动运行也生效）.
func Restart(exePath string) error {
	return syscall.Exec(exePath, os.Args, os.Environ())
}
