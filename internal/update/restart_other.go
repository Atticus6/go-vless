//go:build !unix

package update

import "errors"

// Restart 非 unix 平台不支持原地重启，请手动替换更新.
func Restart(exePath string) error {
	return errors.New("self-update restart not supported on this OS")
}
