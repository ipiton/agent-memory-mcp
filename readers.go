package main

import (
	"fmt"
	"io"
	"os"
)

func readTextFile(path string, offset int64, maxBytes int64, size int64) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	// Seek to offset if specified
	if offset > 0 {
		if offset >= size {
			return "", false, fmt.Errorf("offset %d exceeds file size %d", offset, size)
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return "", false, fmt.Errorf("failed to seek to offset %d: %w", offset, err)
		}
	}

	var reader io.Reader = file
	truncated := false
	remainingBytes := size - offset
	if maxBytes > 0 && remainingBytes > maxBytes {
		truncated = true
		reader = io.LimitReader(file, maxBytes)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", truncated, err
	}
	if isBinary(data) {
		return "", truncated, fmt.Errorf("binary files are not supported")
	}

	if truncated {
		return fmt.Sprintf("(truncated to %d bytes from offset %d)\n\n%s", maxBytes, offset, string(data)), truncated, nil
	}

	return string(data), truncated, nil
}
