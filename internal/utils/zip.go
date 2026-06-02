package utils

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxUploadFileSize = 200 * 1024 * 1024 // 200MB
	MaxUnzippedSize   = 500 * 1024 * 1024 // 500MB
)

func Unzip(src, dest string) error {
	return unzipWithLimit(src, dest, MaxUnzippedSize)
}

func unzipWithLimit(src, dest string, maxTotalSize int64) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	var totalSize int64
	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		written, err := io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}

		totalSize += written
		if totalSize > maxTotalSize {
			return fmt.Errorf("unzipped size exceeds %dMB", maxTotalSize/(1024*1024))
		}
	}
	return nil
}
