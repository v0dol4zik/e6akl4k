package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// createZIP stores the results' files in a new archive at path. With consume, each file is
// removed once it is in the archive, so packing a batch never takes twice its size on disk.
func createZIP(path string, results []downloadResult, consume bool) (returnErr error) {
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
		name = sanitizeArchiveName(name + extension)
		baseName := name
		for duplicate := 1; used[name]; duplicate++ {
			name = fmt.Sprintf("%02d-%02d - %s", i+1, duplicate, baseName)
		}
		used[name] = true
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		entry, err := writer.CreateHeader(header)
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
		if consume {
			_ = os.Remove(result.FilePath)
		}
	}
	return nil
}

func sanitizeArchiveName(name string) string {
	name = unsafeName.ReplaceAllString(name, "_")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		return "track"
	}
	return shortenRunes(name, 180)
}
