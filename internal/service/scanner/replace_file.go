//go:build !windows

package scanner

import "os"

func replaceFile(tmpPath, path string) error {
	return os.Rename(tmpPath, path)
}
