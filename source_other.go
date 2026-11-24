//go:build !windows

package main

import (
	"errors"
	"os"
)

func openSource(path string, _ bool) (*os.File, error) {
	return os.Open(path)
}

func sourceStillAtPath(file *os.File, expected os.FileInfo) (bool, error) {
	actual, err := os.Lstat(file.Name())
	if err != nil {
		return false, err
	}
	return sameMetadata(expected, actual) && os.SameFile(expected, actual), nil
}

func removeSource(file *os.File) error {
	return os.Remove(file.Name())
}

const desktopAvailable = false

func attachParentConsole() {}

func showDesktopError(_ error) {}

func openDesktop(_, _ string) error {
	return errors.New("indstillingsvinduet kræver Windows")
}

func archiveRunLock(_ string) (func(), error) {
	return func() {}, nil
}

func runWindowsService(_, _ string) error {
	return errors.New("tjenestetilstand kræver Windows")
}

func installWindowsService(_, _ string) error {
	return errors.New("installation af tjenesten kræver Windows")
}
