package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

type serviceExit struct {
	specific bool
	code     uint32
}

func startArchiveHandler(run func(context.Context) error) (chan<- svc.ChangeRequest, <-chan svc.Status, <-chan serviceExit) {
	requests := make(chan svc.ChangeRequest, 2)
	status := make(chan svc.Status, 4)
	exit := make(chan serviceExit, 1)
	go func() {
		specific, code := (archiveService{run: run}).Execute(nil, requests, status)
		exit <- serviceExit{specific, code}
	}()
	return requests, status, exit
}

func serviceResult[T any](t *testing.T, data <-chan T) T {
	t.Helper()
	select {
	case value := <-data:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for service")
		var zero T
		return zero
	}
}

func TestWindowsServiceStopsArchiveWorker(t *testing.T) {
	for _, command := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		requests, status, exit := startArchiveHandler(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if s := serviceResult(t, status); s.State != svc.StartPending {
			t.Fatalf("startup status: %#v", s)
		}
		if s := serviceResult(t, status); s.State != svc.Running || s.Accepts != svc.AcceptStop|svc.AcceptShutdown {
			t.Fatalf("running status: %#v", s)
		}
		requests <- svc.ChangeRequest{Cmd: svc.Interrogate}
		if s := serviceResult(t, status); s.State != svc.Running {
			t.Fatalf("interrogated status: %#v", s)
		}
		requests <- svc.ChangeRequest{Cmd: command}
		if s := serviceResult(t, status); s.State != svc.StopPending {
			t.Fatalf("shutdown status: %#v", s)
		}
		if result := serviceResult(t, exit); result != (serviceExit{}) {
			t.Fatalf("service stop was reported as failure: %#v", result)
		}
	}
}

func TestWindowsServiceReportsStartupFailure(t *testing.T) {
	_, _, exit := startArchiveHandler(func(context.Context) error { return errors.New("missing credentials") })
	if result := serviceResult(t, exit); !result.specific || result.code == 0 {
		t.Fatalf("service startup failure was not reported: %#v", result)
	}
}
