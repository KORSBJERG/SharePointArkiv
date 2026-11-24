package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const retention = 30 * 24 * time.Hour

type remoteFolder interface {
	lookup(context.Context, string) (driveItem, error)
	upload(context.Context, string, io.ReaderAt, int64) (driveItem, error)
	download(context.Context, string) (io.ReadCloser, error)
}

type archiveStamp struct {
	modified time.Time
	size     int64
	id       string
	etag     string
	ready    bool
}

type archiver struct {
	cfg      config
	logger   *log.Logger
	remote   remoteFolder
	verified map[string]archiveStamp
}

func (a *archiver) scan(ctx context.Context, now time.Time) error {
	entries, err := os.ReadDir(a.cfg.LocalFolder)
	if err != nil {
		return fmt.Errorf("læs kildemappen: %w", err)
	}
	next := make(map[string]archiveStamp)
	var checked, waiting, failed int
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		info, err := entry.Info()
		if err == nil {
			if !info.Mode().IsRegular() {
				continue
			}
			if now.Sub(info.ModTime()) < time.Duration(a.cfg.MinimumFileAgeSeconds)*time.Second {
				waiting++
				continue
			}
			checked++
			var stamp archiveStamp
			stamp, err = a.processFile(ctx, filepath.Join(a.cfg.LocalFolder, name), info, now)
			if stamp.id != "" {
				next[name] = stamp
			}
		}
		if err != nil {
			failed++
			a.logger.Printf("Beholdt %q lokalt: %v", name, err)
			if hasStatus(err, http.StatusTooManyRequests) || hasStatus(err, http.StatusServiceUnavailable) {
				// Wait for the next scan before sending more requests.
				return fmt.Errorf("gennemgangen blev afbrudt: %w", err)
			}
		}
	}
	a.verified = next
	a.logger.Printf("Gennemgang færdig. Behandlede filer: %d. Afventer: %d. Fejl: %d.", checked, waiting, failed)
	if failed != 0 {
		return fmt.Errorf("%d filer kunne ikke behandles. Se arkiv.log", failed)
	}
	return nil
}

func (a *archiver) processFile(ctx context.Context, path string, info os.FileInfo, now time.Time) (archiveStamp, error) {
	var stamp archiveStamp
	name := info.Name()
	old := info.ModTime().Before(now.Add(-retention))
	deleteLocal := old && a.cfg.DeleteLocal
	file, err := openSource(path, deleteLocal)
	if err != nil {
		return stamp, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return stamp, err
	}
	if !sameMetadata(info, current) {
		return stamp, errors.New("filen ændrede sig før behandling")
	}

	item, err := a.remote.lookup(ctx, name)
	if hasStatus(err, http.StatusNotFound) {
		a.logger.Printf("Kopierer til SharePoint: %q (%d byte)", name, info.Size())
		item, err = a.remote.upload(ctx, name, file, info.Size())
		switch {
		case err == nil:
			if !matchesMetadata(item, info) {
				return stamp, errors.New("SharePoint bekræftede ikke den forventede fil efter upload")
			}
			a.logger.Printf("Kopieret til SharePoint: %q", name)
			if !old {
				return newStamp(info, item, false), nil
			}
		case hasStatus(err, http.StatusConflict), hasStatus(err, http.StatusPreconditionFailed):
			// Another writer may have created the file between lookup and upload.
			item, err = a.remote.lookup(ctx, name)
		}
	}
	if err != nil {
		return stamp, err
	}
	if !matchesMetadata(item, info) {
		return stamp, errors.New("navnekonflikt i SharePoint")
	}
	if item.ETag == "" {
		return stamp, errors.New("SharePoint returnerede filen uden en versionsidentifikator")
	}
	stamp = newStamp(info, item, old)
	if !deleteLocal && a.verified[name] == stamp {
		return stamp, nil
	}
	a.logger.Printf("Kontrollerer SharePoint-kopi: %q", name)
	if err := a.verify(ctx, file, info, item); err != nil {
		return archiveStamp{}, err
	}
	if !deleteLocal {
		if old {
			a.logger.Printf("Klar til lokal sletning: %q (delete_local er false)", name)
		}
		return stamp, nil
	}

	if err := ctx.Err(); err != nil {
		return archiveStamp{}, err
	}
	current, err = file.Stat()
	if err != nil {
		return archiveStamp{}, err
	}
	if !sameMetadata(info, current) {
		return archiveStamp{}, errors.New("den lokale fil ændrede sig under kontrollen")
	}
	sameFile, err := sourceStillAtPath(file, current)
	if err != nil {
		return archiveStamp{}, err
	}
	if !sameFile {
		return archiveStamp{}, errors.New("filen på den lokale sti blev erstattet under kontrollen")
	}
	if err := removeSource(file); err != nil {
		return archiveStamp{}, fmt.Errorf("lokal sletning: %w", err)
	}
	a.logger.Printf("Slettet lokalt efter kontrol i SharePoint: %q", name)
	return archiveStamp{}, nil
}

func newStamp(info os.FileInfo, item driveItem, ready bool) archiveStamp {
	return archiveStamp{modified: info.ModTime(), size: info.Size(), id: item.ID, etag: item.ETag, ready: ready}
}

func matchesMetadata(item driveItem, info os.FileInfo) bool {
	return item.ID != "" && item.File != nil && item.Size == info.Size() && strings.EqualFold(item.Name, info.Name())
}

func sameMetadata(a, b os.FileInfo) bool {
	return b.Mode().IsRegular() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (a *archiver) verify(ctx context.Context, file *os.File, info os.FileInfo, item driveItem) error {
	local := sha256.New()
	if _, err := io.Copy(local, io.NewSectionReader(file, 0, info.Size())); err != nil {
		return err
	}
	body, err := a.remote.download(ctx, item.ID)
	if err != nil {
		return err
	}
	defer body.Close()
	remote := sha256.New()
	size, err := io.Copy(remote, io.LimitReader(body, info.Size()+1))
	if err != nil {
		return fmt.Errorf("kontrollér filens indhold i SharePoint: %w", err)
	}
	if size != info.Size() || !bytes.Equal(local.Sum(nil), remote.Sum(nil)) {
		return errors.New("navnekonflikt: filerne har forskelligt indhold")
	}
	latest, err := a.remote.lookup(ctx, info.Name())
	if err != nil {
		return err
	}
	if !matchesMetadata(latest, info) || latest.ID != item.ID || latest.ETag != item.ETag {
		return errors.New("SharePoint-filen ændrede sig under kontrollen")
	}
	return nil
}
