package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func createZIP(path string, results []downloadResult) (returnErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	writer := zip.NewWriter(file)
	defer func() {
		if err := writer.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()

	used := make(map[string]bool)
	for i, result := range results {
		extension := filepath.Ext(result.FilePath)
		name := result.Title
		if name == "" {
			name = filepath.Base(result.FilePath)
			name = name[:len(name)-len(extension)]
		}
		if result.Artist != "" {
			name = result.Artist + " - " + name
		}
		name = unsafeName.ReplaceAllString(name+extension, "_")
		if used[name] {
			name = fmt.Sprintf("%02d - %s", i+1, name)
		}
		used[name] = true
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		source, err := os.Open(result.FilePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
