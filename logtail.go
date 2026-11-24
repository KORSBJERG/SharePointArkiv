package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

const archiveLogName = "arkiv.log"
const logViewBytes = 128 * 1024

// logTail reads a bounded snapshot from the end of the current log file.
type logTail struct {
	path     string
	previous os.FileInfo
}

func (tail *logTail) read() (string, bool, error) {
	file, err := os.Open(tail.path)
	if err != nil {
		tail.previous = nil
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		tail.previous = nil
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		tail.previous = nil
		return "", false, fmt.Errorf("%s er ikke en almindelig fil", archiveLogName)
	}
	if old := tail.previous; old != nil && old.Size() == info.Size() &&
		old.ModTime().Equal(info.ModTime()) && os.SameFile(old, info) {
		return "", false, nil
	}

	start := max(int64(0), info.Size()-logViewBytes-1)
	data, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	if err != nil {
		tail.previous = nil
		return "", false, err
	}
	// Keep only complete lines. A concurrent writer may still be writing the last one.
	if start > 0 {
		if first := bytes.IndexByte(data, '\n'); first >= 0 {
			data = data[first+1:]
		}
	}
	data = data[:bytes.LastIndexByte(data, '\n')+1]
	tail.previous = info
	return string(data), true, nil
}
