package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryFolder struct {
	content           map[string][]byte
	uploadFailure     error
	downloadFailure   error
	failAfterDownload bool
	downloads         int
	uploads           int
}

func (f *memoryFolder) lookup(_ context.Context, name string) (driveItem, error) {
	data, found := f.content[name]
	if !found {
		return driveItem{}, &graphError{status: http.StatusNotFound}
	}
	etag := "first"
	if f.failAfterDownload && f.downloads > 0 {
		etag = "changed"
	}
	return driveItem{ID: name, Name: name, ETag: etag, File: &struct{}{}, Size: int64(len(data))}, nil
}

func (f *memoryFolder) upload(ctx context.Context, name string, source io.ReaderAt, size int64) (driveItem, error) {
	if f.uploadFailure != nil {
		return driveItem{}, f.uploadFailure
	}
	data, err := io.ReadAll(io.NewSectionReader(source, 0, size))
	if err != nil {
		return driveItem{}, err
	}
	f.uploads++
	f.content[name] = data
	return f.lookup(ctx, name)
}

func (f *memoryFolder) download(_ context.Context, name string) (io.ReadCloser, error) {
	if f.downloadFailure != nil {
		return nil, f.downloadFailure
	}
	f.downloads++
	return io.NopCloser(bytes.NewReader(f.content[name])), nil
}

func TestArchivePreservesFilesUntilVerified(t *testing.T) {
	const name = "eksport #1.csv"
	const content = "id,navn\n1,A\n"
	tests := []struct {
		name            string
		age             time.Duration
		deleteLocal     bool
		remoteContent   string
		uploadFailure   error
		downloadFailure error
		remoteChanges   bool
		wantDelete      bool
		wantRemote      string
		wantError       string
	}{
		{name: "new", age: time.Hour, deleteLocal: true, wantRemote: content},
		{name: "still being produced", age: time.Second, deleteLocal: true},
		{name: "exactly 30 days", age: retention, deleteLocal: true, wantRemote: content},
		{name: "old source with no cloud copy", age: retention + time.Hour, deleteLocal: true, wantDelete: true, wantRemote: content},
		{name: "copy only", age: retention + time.Hour, wantRemote: content},
		{name: "identical cloud copy", age: retention + time.Hour, deleteLocal: true, remoteContent: content, wantDelete: true, wantRemote: content},
		{name: "same name and size different content", age: retention + time.Hour, deleteLocal: true, remoteContent: "id,navn\n1,B\n", wantRemote: "id,navn\n1,B\n", wantError: "navnekonflikt"},
		{name: "upload interrupted", age: retention + time.Hour, deleteLocal: true, uploadFailure: errors.New("interrupted"), wantError: "interrupted"},
		{name: "download unavailable", age: retention + time.Hour, deleteLocal: true, remoteContent: content, downloadFailure: errors.New("unavailable"), wantRemote: content, wantError: "unavailable"},
		{name: "cloud version changes during verification", age: retention + time.Hour, deleteLocal: true, remoteContent: content, remoteChanges: true, wantRemote: content, wantError: "SharePoint-filen ændrede sig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			stamp := now.Add(-tt.age)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			remote := &memoryFolder{
				content:           map[string][]byte{},
				uploadFailure:     tt.uploadFailure,
				downloadFailure:   tt.downloadFailure,
				failAfterDownload: tt.remoteChanges,
			}
			if tt.remoteContent != "" {
				remote.content[name] = []byte(tt.remoteContent)
			}
			var logs strings.Builder
			a := archiver{
				cfg:    config{LocalFolder: dir, MinimumFileAgeSeconds: 120, DeleteLocal: tt.deleteLocal},
				logger: log.New(&logs, "", 0),
				remote: remote,
			}
			if err := a.scan(t.Context(), now); (err != nil) != (tt.wantError != "") || !strings.Contains(logs.String(), tt.wantError) {
				t.Fatalf("unexpected scan result %v\n%s", err, logs.String())
			}
			data, err := os.ReadFile(path)
			if tt.wantDelete {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("expected local deletion, read returned %v", err)
				}
			} else if err != nil || string(data) != content {
				t.Fatalf("original should be preserved, got %q, %v", data, err)
			}
			if got := string(remote.content[name]); got != tt.wantRemote {
				t.Fatalf("remote content %q, want %q", got, tt.wantRemote)
			}
			if tt.remoteContent != "" && remote.uploads != 0 {
				t.Fatal("unexpected upload to an existing filename")
			}
		})
	}
}
