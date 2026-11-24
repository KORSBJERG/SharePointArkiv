package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const archiveServiceName = "PeakNetSharePointArkiv"
const archiveServiceAccount = `NT SERVICE\` + archiveServiceName

type archiveService struct {
	run func(context.Context) error
}

func runWindowsService(filename, executable string) error {
	// SCM launches the service from a Windows system directory. Use an absolute
	// configuration path before starting any file work.
	filename, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	err = svc.Run(archiveServiceName, archiveService{
		run: func(ctx context.Context) error {
			resolved, err := resolveConfigPath(filename)
			if err != nil {
				return fmt.Errorf("find indstillingsfilen: %w", err)
			}
			return runConfiguredArchive(ctx, resolved, executable, runWatch, io.Discard)
		},
	})
	if errors.Is(err, windows.ERROR_FAILED_SERVICE_CONTROLLER_CONNECT) {
		return errors.New("start tjenesten fra Windows Tjenester eller med Start-Service")
	}
	return err
}

func (service archiveService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 5000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- service.run(ctx) }()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	var ticker *time.Ticker
	var progress <-chan time.Time
	for {
		select {
		case err := <-done:
			if ticker != nil {
				ticker.Stop()
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				requests = nil
			}
			if request.Cmd == svc.Interrogate {
				status <- current
			}
			if (!ok || request.Cmd == svc.Stop || request.Cmd == svc.Shutdown) && current.State != svc.StopPending {
				current = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 5000}
				status <- current
				cancel()
				ticker = time.NewTicker(2 * time.Second)
				progress = ticker.C
			}
		case <-progress:
			current.CheckPoint++
			status <- current
		}
	}
}
