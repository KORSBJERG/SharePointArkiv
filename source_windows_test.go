package main

import "testing"

func TestArchiveRunLockPreventsConcurrentWorkers(t *testing.T) {
	dir := t.TempDir()
	release, err := archiveRunLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	if duplicate, err := archiveRunLock(dir); err == nil {
		duplicate()
		t.Fatal("a second worker acquired the same lock")
	}
	release()
	release = nil
	reopened, err := archiveRunLock(dir)
	if err != nil {
		t.Fatal("worker could not restart:", err)
	}
	reopened()
}
