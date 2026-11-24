package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func installWindowsService(filename, executable string) (err error) {
	cfg, err := loadArchiveConfig(filename, executable)
	if err != nil {
		return fmt.Errorf("gem indstillingerne i appen før installation: %w", err)
	}
	appDir := filepath.Dir(filename)
	if _, err := loadCredential(cfg, appDir); err != nil {
		return fmt.Errorf("kontrollér klientcertifikatet før installation: %w", err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("åbn Windows Tjenester fra PowerShell som administrator: %w", err)
	}
	defer manager.Disconnect()

	service, err := manager.CreateService(archiveServiceName, executable, mgr.Config{
		DisplayName:      applicationName,
		Description:      "Kopierer lokale filer til SharePoint Online med PeakNet SharePoint Arkiv.",
		ServiceStartName: archiveServiceAccount,
		StartType:        mgr.StartManual,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "-service", "-config", filename)
	if errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
		return errors.New("tjenesten findes allerede. Administrér den i Windows Tjenester")
	}
	if err != nil {
		return fmt.Errorf("opret Windows-tjenesten: %w", err)
	}
	defer service.Close()
	defer func() {
		if err != nil {
			if deleteErr := service.Delete(); deleteErr != nil {
				err = errors.Join(err, fmt.Errorf("fjern ufuldstændig tjenesteregistrering: %w", deleteErr))
			}
		}
	}()

	sid, _, _, err := windows.LookupSID("", archiveServiceAccount)
	if err != nil {
		return fmt.Errorf("find tjenestens konto: %w", err)
	}
	if err := grantArchiveServiceAccess(appDir, filename, executable, sid); err != nil {
		return err
	}
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Minute},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
		{Type: mgr.NoAction},
	}, 24*60*60); err != nil {
		return fmt.Errorf("indstil genstart ved fejl: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("indstil genstart ved fejl: %w", err)
	}
	settings, err := service.Config()
	if err != nil {
		return fmt.Errorf("læs tjenestens indstillinger: %w", err)
	}
	settings.StartType = mgr.StartAutomatic
	settings.DelayedAutoStart = true
	if err := service.UpdateConfig(settings); err != nil {
		return fmt.Errorf("indstil automatisk start: %w", err)
	}
	fmt.Printf("Tjenesten %s er installeret. Giv %s adgang til %s, før du starter tjenesten.\n",
		archiveServiceName, archiveServiceAccount, cfg.LocalFolder)
	return nil
}

func grantArchiveServiceAccess(dir, filename, executable string, sid *windows.SID) error {
	const read = windows.FILE_GENERIC_READ
	const readExecute = read | windows.FILE_GENERIC_EXECUTE
	paths := []struct {
		path        string
		rights      uint32
		inheritance uint32
	}{
		// Replacement configuration files created by the GUI inherit read access.
		{dir, readExecute, windows.OBJECT_INHERIT_ACE | windows.NO_PROPAGATE_INHERIT_ACE},
		{executable, readExecute, 0},
		{filename, read, 0},
		{filepath.Join(dir, certName), read, 0},
		{filepath.Join(dir, keyName), read, 0},
	}
	for _, path := range paths {
		if err := grantServiceFileAccess(path.path, sid, path.rights, path.inheritance); err != nil {
			return err
		}
	}
	for _, name := range []string{archiveLogName, archiveRunLockName} {
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if err := file.Close(); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := grantServiceFileAccess(path, sid, read|windows.FILE_GENERIC_WRITE, 0); err != nil {
			return err
		}
	}
	return nil
}

func grantServiceFileAccess(path string, sid *windows.SID, rights, inheritance uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	wantDirectory := inheritance != 0
	if (wantDirectory && !info.IsDir()) || (!wantDirectory && !info.Mode().IsRegular()) {
		return fmt.Errorf("kan ikke tildele tjenesten adgang til %s med denne filtype", path)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("læs Windows-rettigheder for %s: %w", path, err)
	}
	current, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("læs Windows-rettigheder for %s: %w", path, err)
	}
	if current == nil {
		return fmt.Errorf("angiv en adgangsliste for %s før installation", path)
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.ACCESS_MASK(rights),
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, current)
	if err != nil {
		return fmt.Errorf("beregn Windows-rettigheder for %s: %w", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("tildel tjenesten adgang til %s: %w", path, err)
	}
	return nil
}
