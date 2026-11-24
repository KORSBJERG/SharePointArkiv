package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const applicationName = "PeakNet SharePoint Arkiv"
const archiveRunLockName = "arkiv.korsel"

type config struct {
	LocalFolder           string `json:"local_folder"`
	SiteURL               string `json:"site_url"`
	DocumentLibrary       string `json:"document_library"`
	DriveID               string `json:"drive_id"`
	DestinationFolder     string `json:"destination_folder"`
	TenantID              string `json:"tenant_id"`
	ClientID              string `json:"client_id"`
	IntervalMinutes       int    `json:"scan_interval_minutes"`
	MinimumFileAgeSeconds int    `json:"minimum_file_age_seconds"`
	DeleteLocal           bool   `json:"delete_local"`
}

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func defaultConfig() config {
	return config{IntervalMinutes: 5, MinimumFileAgeSeconds: 120}
}

func readConfig(filename string) (config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(filename)
	if err != nil {
		return cfg, err
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("Læser indstillinger: %w", err)
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return cfg, errors.New("indstillingsfilen skal indeholde ét JSON-objekt")
	}
	return cfg, nil
}

func saveConfig(filename string, cfg config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(filename), "arkiv-indstillinger-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(append(data, '\n'))
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filename)
}

func resolveConfigPath(path string) (string, error) {
	filename, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(filename))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(filename)), nil
}

func validateLocalConfig(cfg config, filename, executable string) (config, error) {
	if !filepath.IsAbs(cfg.LocalFolder) {
		return cfg, errors.New("Vælg en lokal kildemappe med en fuld sti.")
	}
	source, err := filepath.EvalSymlinks(cfg.LocalFolder)
	if err != nil {
		return cfg, fmt.Errorf("åbn kildemappen: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return cfg, errors.New("Den lokale kildemappe skal findes.")
	}
	cfg.LocalFolder = source
	for _, path := range []string{filename, executable, filepath.Join(filepath.Dir(filename), keyName)} {
		resolved, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			resolved = path
		} else if err != nil {
			return cfg, err
		}
		relative, err := filepath.Rel(source, resolved)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return cfg, errors.New("programmet, indstillingerne og den private nøgle skal ligge uden for kildemappen")
		}
	}
	if cfg.IntervalMinutes < 1 || cfg.IntervalMinutes > 1440 ||
		cfg.MinimumFileAgeSeconds < 0 || cfg.MinimumFileAgeSeconds > 86400 {
		return cfg, errors.New("Brug 1–1440 minutter for intervallet og 0–86400 sekunder for ventetiden.")
	}
	return cfg, nil
}

func validateConfig(cfg config, filename, executable string) (config, error) {
	cfg, err := validateLocalConfig(cfg, filename, executable)
	if err != nil {
		return cfg, err
	}
	site, err := url.Parse(cfg.SiteURL)
	if err != nil || site.Scheme != "https" || site.Host != site.Hostname() ||
		!strings.HasSuffix(strings.ToLower(site.Host), ".sharepoint.com") ||
		site.User != nil || site.RawQuery != "" || site.Fragment != "" {
		return cfg, errors.New("Indtast adressen på SharePoint-webstedet, fx https://firma.sharepoint.com/sites/Drift.")
	}
	if strings.TrimSpace(cfg.DocumentLibrary) == "" && cfg.DriveID == "" {
		return cfg, errors.New("Indtast dokumentbibliotekets navn eller dets Graph-id.")
	}
	cfg.DestinationFolder = strings.Trim(cfg.DestinationFolder, "/")
	if cfg.DestinationFolder != "" {
		for _, part := range strings.Split(cfg.DestinationFolder, "/") {
			if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") {
				return cfg, errors.New("Brug / mellem undermapper i feltet Mappe i biblioteket.")
			}
		}
	}
	if !uuidPattern.MatchString(cfg.TenantID) || !uuidPattern.MatchString(cfg.ClientID) {
		return cfg, errors.New("Indtast lejer-id og program-id fra appregistreringen i Microsoft Entra.")
	}
	return cfg, nil
}

type runMode uint8

const (
	runOnce runMode = iota
	runCheck
	runWatch
)

func main() {
	if desktopAvailable && len(os.Args) > 1 {
		attachParentConsole()
	}
	if err := run(); err != nil {
		if desktopAvailable && len(os.Args) == 1 {
			showDesktopError(err)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	configPath := flag.String("config", filepath.Join(filepath.Dir(executable), "config.json"), "sti til indstillinger")
	watch := flag.Bool("watch", false, "fortsæt med at tjekke mappen")
	check := flag.Bool("check", false, "kontrollér adgang til destinationsmappen")
	once := flag.Bool("once", false, "kør én gennemgang")
	gui := flag.Bool("gui", desktopAvailable && len(os.Args) == 1, "åbn indstillingsvinduet")
	newCert := flag.Bool("new-certificate", false, "opret et klientcertifikat ved siden af indstillingerne")
	service := flag.Bool("service", false, "kør via Windows Tjenester")
	installService := flag.Bool("install-service", false, "installér Windows-tjenesten som administrator")
	flag.Parse()
	var actions int
	for _, set := range []bool{*watch, *check, *once, *gui, *newCert, *service, *installService} {
		if set {
			actions++
		}
	}
	if flag.NArg() != 0 || actions > 1 {
		return errors.New("vælg én kørselstilstand")
	}
	if *service {
		return runWindowsService(*configPath, executable)
	}
	filename, err := resolveConfigPath(*configPath)
	if err != nil {
		return fmt.Errorf("find indstillingsfilen: %w", err)
	}
	if *gui {
		return openDesktop(executable, filename)
	}
	if *installService {
		return installWindowsService(filename, executable)
	}
	if *newCert {
		appDir := filepath.Dir(filename)
		if err := generateCertificate(appDir); err != nil {
			return err
		}
		fmt.Printf("Upload %s til appregistreringen i Microsoft Entra.\n", filepath.Join(appDir, certName))
		return nil
	}
	mode := runOnce
	switch {
	case *watch:
		mode = runWatch
	case *check:
		mode = runCheck
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runConfiguredArchive(ctx, filename, executable, mode, os.Stdout)
}

func loadArchiveConfig(filename, executable string) (config, error) {
	cfg, err := readConfig(filename)
	if err != nil {
		return cfg, err
	}
	return validateConfig(cfg, filename, executable)
}

func runConfiguredArchive(ctx context.Context, filename, executable string, mode runMode, output io.Writer) error {
	appDir := filepath.Dir(filename)
	cfg, err := loadArchiveConfig(filename, executable)
	if err != nil {
		if logger, file, logErr := openArchiveLog(appDir, io.Discard); logErr == nil {
			logger.Printf("Kørslen kunne ikke starte: %v", err)
			file.Close()
		}
		return err
	}
	return runArchive(ctx, cfg, appDir, mode, output)
}

func openArchiveLog(appDir string, output io.Writer) (*log.Logger, *os.File, error) {
	file, err := os.OpenFile(filepath.Join(appDir, archiveLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("åbn %s: %w", archiveLogName, err)
	}
	return log.New(io.MultiWriter(file, output), "", log.Ldate|log.Ltime), file, nil
}

func runArchive(ctx context.Context, cfg config, appDir string, mode runMode, output io.Writer) error {
	logger, logFile, err := openArchiveLog(appDir, output)
	if err != nil {
		return err
	}
	defer logFile.Close()
	if mode != runCheck {
		release, err := archiveRunLock(appDir)
		if err != nil {
			logger.Print(err)
			return err
		}
		defer release()
	}
	switch mode {
	case runCheck:
		logger.Print("Tester forbindelsen til SharePoint.")
	case runWatch:
		logger.Printf("Overvågning startet. Interval: %d min.", cfg.IntervalMinutes)
		defer logger.Print("Overvågning afsluttet.")
	}
	credential, err := loadCredential(cfg, appDir)
	if err != nil {
		logger.Print(err)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("Opret et klientcertifikat, og følg Entra-opsætningen i vejledningen.")
		}
		return err
	}
	graph := newGraph(credential)
	if mode == runCheck {
		folder, err := graph.findFolder(ctx, cfg)
		if err != nil {
			logger.Print(err)
			return err
		}
		logger.Printf("SharePoint-mappen er tilgængelig: %s", folder.webURL)
		return nil
	}

	var archive archiver
	archive.cfg = cfg
	archive.logger = logger
	cleanup := "fra"
	if cfg.DeleteLocal {
		cleanup = "til"
	}
	logger.Printf("Kilde: %s. Lokal sletning: %s.", cfg.LocalFolder, cleanup)
	var lastDestination string
	for {
		if ctx.Err() != nil {
			break
		}
		logger.Print("Starter gennemgang. Finder destinationen i SharePoint.")
		folder, err := graph.findFolder(ctx, cfg)
		if err == nil {
			archive.remote = folder
			if folder.webURL != lastDestination {
				logger.Printf("Destination: %s", folder.webURL)
				lastDestination = folder.webURL
			}
			err = archive.scan(ctx, time.Now())
		}
		if err != nil && ctx.Err() == nil {
			logger.Print(err)
			if mode != runWatch {
				return err
			}
		}
		if mode != runWatch {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			break
		}
		logger.Printf("Næste gennemgang om %d min.", cfg.IntervalMinutes)
		timer := time.NewTimer(time.Duration(cfg.IntervalMinutes) * time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	if mode == runWatch {
		return nil
	}
	return ctx.Err()
}
