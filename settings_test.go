package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsReadOldFileAndSaveFromDesktop(t *testing.T) {
	appDir := t.TempDir()
	source := t.TempDir()
	path := filepath.Join(appDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"document_library":"Dokumenter","delete_local":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalMinutes != 5 || cfg.MinimumFileAgeSeconds != 120 || !cfg.DeleteLocal {
		t.Fatalf("existing settings were not restored: %+v", cfg)
	}
	cfg.LocalFolder = source
	cfg.SiteURL = "https://firma.sharepoint.com/sites/Drift"
	cfg.DestinationFolder = "Arkiv/Årsrapporter"
	cfg.TenantID = "00000000-0000-0000-0000-000000000001"
	cfg.ClientID = "00000000-0000-0000-0000-000000000002"
	cfg.IntervalMinutes = 7
	cfg.DeleteLocal = false
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = validateConfig(cfg, path, executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if loaded, err := readConfig(path); err != nil || loaded != cfg {
		t.Fatalf("saved settings were not restored: %+v (%v)", loaded, err)
	}
	cfg.LocalFolder = appDir
	if _, err := validateConfig(cfg, path, executable); err == nil {
		t.Fatal("settings folder must not be accepted as source")
	}
}
