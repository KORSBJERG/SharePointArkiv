package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const microsoftGraphAppID = "00000003-0000-0000-c000-000000000000"

type setupLink struct {
	site *url.URL
	path string
}

func parseSetupLink(raw string) (setupLink, error) {
	link, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !isSharePointURL(link) {
		return setupLink{}, errors.New("Indsæt adressen fra en mappe i SharePoint Online.")
	}
	path := link.Path
	// SharePoint may represent the opened folder as id or RootFolder on a view page.
	for _, param := range []string{"id", "RootFolder"} {
		if value := link.Query().Get(param); value != "" {
			if !strings.HasPrefix(value, "/") {
				return setupLink{}, errors.New("Åbn mappen i SharePoint, og kopiér adresselinjen.")
			}
			path = value
			break
		}
	}
	if strings.HasPrefix(path, "/:f:/r/") {
		path = strings.TrimPrefix(path, "/:f:/r")
	}
	if strings.HasSuffix(strings.ToLower(path), "/forms/allitems.aspx") {
		path = path[:len(path)-len("/forms/allitems.aspx")]
	}
	path = strings.TrimRight(path, "/")
	parts := pathParts(path)
	if len(parts) == 0 || strings.HasPrefix(parts[0], ":") || strings.EqualFold(parts[0], "personal") {
		return setupLink{}, errors.New("Åbn selve mappen i SharePoint, og kopiér adresselinjen til guiden.")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\:\x00\r\n") {
			return setupLink{}, errors.New("Mappestien i SharePoint-linket er ugyldig.")
		}
	}
	site := &url.URL{Scheme: "https", Host: strings.ToLower(link.Host)}
	if strings.EqualFold(parts[0], "sites") || strings.EqualFold(parts[0], "teams") {
		if len(parts) < 3 {
			return setupLink{}, errors.New("Åbn en mappe eller et dokumentbibliotek på SharePoint-webstedet.")
		}
		site.Path = "/" + strings.Join(parts[:2], "/")
	}
	return setupLink{site: site, path: path}, nil
}

func isSharePointURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Host == u.Hostname() &&
		strings.HasSuffix(strings.ToLower(u.Host), ".sharepoint.com") &&
		!strings.EqualFold(u.Host, ".sharepoint.com")
}

func pathParts(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func sharePointRelative(path, root string) (string, bool) {
	parts, prefix := pathParts(path), pathParts(root)
	if len(parts) < len(prefix) {
		return "", false
	}
	for i, part := range prefix {
		if !strings.EqualFold(parts[i], part) {
			return "", false
		}
	}
	return strings.Join(parts[len(prefix):], "/"), true
}

func (link setupLink) matches(webURL string) bool {
	actual, err := url.Parse(webURL)
	if err != nil || !isSharePointURL(actual) || !strings.EqualFold(actual.Host, link.site.Host) {
		return false
	}
	relative, matches := sharePointRelative(actual.Path, link.path)
	return matches && relative == ""
}

type setupGraph interface {
	request(context.Context, string, string, any, any) error
}

type setupTarget struct {
	siteID, driveID, library, folder string
}

func findSetupTarget(ctx context.Context, admin setupGraph, link setupLink) (setupTarget, error) {
	var site struct{ ID, WebURL string }
	if err := admin.request(ctx, http.MethodGet, graphSitePath(link.site)+"?$select=id,webUrl", nil, &site); err != nil {
		return setupTarget{}, fmt.Errorf("find SharePoint-webstedet. Kontrollér, at du er logget ind i den rigtige organisation: %w", err)
	}
	canonical, err := url.Parse(site.WebURL)
	if err != nil || !isSharePointURL(canonical) || !strings.EqualFold(canonical.Host, link.site.Host) ||
		!strings.EqualFold(strings.TrimRight(canonical.Path, "/"), link.site.Path) || site.ID == "" {
		return setupTarget{}, errors.New("Microsoft Graph returnerede et andet SharePoint-websted.")
	}
	next := "/sites/" + url.PathEscape(site.ID) + "/drives?$select=id,name,webUrl"
	result := setupTarget{siteID: site.ID}
	longest := 0
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return setupTarget{}, errors.New("Microsoft Graph gentog opslaget af dokumentbiblioteker.")
		}
		seen[next] = true
		var drives struct {
			Value []struct{ ID, Name, WebURL string }
			Next  string `json:"@odata.nextLink"`
		}
		if err := admin.request(ctx, http.MethodGet, next, nil, &drives); err != nil {
			return setupTarget{}, fmt.Errorf("find dokumentbiblioteket: %w", err)
		}
		for _, drive := range drives.Value {
			root, err := url.Parse(drive.WebURL)
			if err != nil || !isSharePointURL(root) || !strings.EqualFold(root.Host, link.site.Host) || drive.ID == "" {
				continue
			}
			folder, ok := sharePointRelative(link.path, root.Path)
			if length := len(pathParts(root.Path)); ok && length > longest {
				longest = length
				result.driveID, result.library, result.folder = drive.ID, drive.Name, folder
			}
		}
		next = drives.Next
	}
	if result.driveID == "" {
		return setupTarget{}, errors.New("Guiden fandt ikke dokumentbiblioteket. Åbn selve mappen i SharePoint, og kopiér adresselinjen.")
	}
	var item driveItem
	if err := admin.request(ctx, http.MethodGet, driveRootPath(result.driveID, result.folder)+"?$select=id,folder,webUrl", nil, &item); err != nil {
		return setupTarget{}, fmt.Errorf("find mappen i dokumentbiblioteket: %w", err)
	}
	if item.ID == "" || item.Folder == nil || !link.matches(item.WebURL) {
		return setupTarget{}, errors.New("Linket skal pege på en eksisterende SharePoint-mappe.")
	}
	return result, nil
}

type setupRegistration struct {
	applicationID, clientID, principalID, permissionID string
	siteID                                             string
}

func createSharePointAccess(ctx context.Context, admin setupGraph, target setupTarget, cred *credential) (setupRegistration, error) {
	reg := setupRegistration{siteID: target.siteID}
	var roles struct {
		ID       string
		AppRoles []struct {
			ID, Value          string
			IsEnabled          bool
			AllowedMemberTypes []string
		}
	}
	if err := admin.request(ctx, http.MethodGet, "/servicePrincipals(appId='"+microsoftGraphAppID+"')?$select=id,appRoles", nil, &roles); err != nil {
		return reg, fmt.Errorf("find Microsoft Graphs programrettigheder: %w", err)
	}
	var roleID string
	for _, role := range roles.AppRoles {
		for _, memberType := range role.AllowedMemberTypes {
			if role.IsEnabled && role.Value == "Sites.Selected" && memberType == "Application" {
				roleID = role.ID
			}
		}
	}
	if !uuidPattern.MatchString(roleID) || !uuidPattern.MatchString(roles.ID) {
		return reg, errors.New("Microsoft Graph returnerede ikke programrettigheden Sites.Selected.")
	}
	cert := cred.certificate
	payload := map[string]any{
		"displayName":    applicationName,
		"signInAudience": "AzureADMyOrg",
		"requiredResourceAccess": []any{map[string]any{
			"resourceAppId":  microsoftGraphAppID,
			"resourceAccess": []any{map[string]string{"id": roleID, "type": "Role"}},
		}},
		"keyCredentials": []any{map[string]any{
			"displayName":   applicationName,
			"type":          "AsymmetricX509Cert",
			"usage":         "Verify",
			"key":           base64.StdEncoding.EncodeToString(cert.Raw),
			"startDateTime": cert.NotBefore.UTC().Format(time.RFC3339),
			"endDateTime":   cert.NotAfter.UTC().Format(time.RFC3339),
		}},
	}
	var app struct{ ID, AppID string }
	err := admin.request(ctx, http.MethodPost, "/applications", payload, &app)
	if uuidPattern.MatchString(app.ID) {
		reg.applicationID = app.ID
	}
	if err != nil {
		return reg, fmt.Errorf("opret Entra-appregistreringen: %w", err)
	}
	if reg.applicationID == "" || !uuidPattern.MatchString(app.AppID) {
		return reg, errors.New("Microsoft Graph returnerede ikke gyldige id'er for Entra-appen.")
	}
	reg.clientID = app.AppID
	var principal struct{ ID string }
	err = retrySetup(ctx, 4, time.Second, func() error {
		return admin.request(ctx, http.MethodPost, "/servicePrincipals", map[string]string{"appId": app.AppID}, &principal)
	}, func(err error) bool { return hasStatus(err, http.StatusNotFound) })
	if uuidPattern.MatchString(principal.ID) {
		reg.principalID = principal.ID
	}
	if err != nil {
		return reg, fmt.Errorf("opret tjenesteidentiteten: %w", err)
	}
	if reg.principalID == "" {
		return reg, errors.New("Microsoft Graph returnerede ikke tjenesteidentitetens id.")
	}
	err = retrySetup(ctx, 4, time.Second, func() error {
		return admin.request(ctx, http.MethodPost, "/servicePrincipals/"+roles.ID+"/appRoleAssignedTo",
			map[string]string{"principalId": principal.ID, "resourceId": roles.ID, "appRoleId": roleID}, nil)
	}, func(err error) bool { return hasStatus(err, http.StatusNotFound) })
	if err != nil {
		return reg, fmt.Errorf("godkend Sites.Selected. Brug en konto med rollen Privileged Role Administrator eller Global Administrator: %w", err)
	}
	var permission struct{ ID string }
	err = admin.request(ctx, http.MethodPost, "/sites/"+url.PathEscape(target.siteID)+"/permissions", map[string]any{
		"roles":               []string{"write"},
		"grantedToIdentities": []any{map[string]any{"application": map[string]string{"id": app.AppID, "displayName": applicationName}}},
	}, &permission)
	reg.permissionID = permission.ID
	if err != nil {
		return reg, fmt.Errorf("giv arkivappen adgang til SharePoint-webstedet: %w", err)
	}
	if reg.permissionID == "" {
		return reg, errors.New("Microsoft Graph returnerede ikke id'et for SharePoint-tildelingen.")
	}
	return reg, nil
}

func (reg setupRegistration) rollback(admin setupGraph) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var err error
	for _, resource := range []struct{ path, id string }{
		{"/sites/" + url.PathEscape(reg.siteID) + "/permissions/", reg.permissionID},
		{"/servicePrincipals/", reg.principalID},
		{"/applications/", reg.applicationID},
	} {
		if resource.id != "" {
			if deleteErr := admin.request(ctx, http.MethodDelete, resource.path+url.PathEscape(resource.id), nil, nil); deleteErr != nil && !hasStatus(deleteErr, http.StatusNotFound) {
				err = errors.Join(err, deleteErr)
			}
		}
	}
	return err
}

func retrySetup(ctx context.Context, attempts int, delay time.Duration, call func() error, retry func(error) bool) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil || attempt == attempts || !retry(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type setupAdminSession interface {
	setupGraph
	close()
	tenantID() string
}

type setupServices struct {
	connect func(context.Context, string, *log.Logger) (setupAdminSession, error)
	verify  func(context.Context, config, setupLink, string, int) error
}

func (services setupServices) configure(ctx context.Context, cfg config, link setupLink, filename, executable string) (err error) {
	cfg, err = validateLocalConfig(cfg, filename, executable)
	if err != nil {
		return err
	}
	dir := filepath.Dir(filename)
	release, err := archiveRunLock(dir)
	if err != nil {
		return fmt.Errorf("stop overvågningen eller Windows-tjenesten, før du ændrer forbindelsen: %w", err)
	}
	defer release()
	oldData, readErr := os.ReadFile(filename)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	logger, file, err := openArchiveLog(dir, io.Discard)
	if err != nil {
		return err
	}
	defer file.Close()
	defer func() {
		if err != nil {
			logger.Printf("SharePoint-opsætning afsluttet med fejl: %v", err)
		}
	}()
	save := func(cfg config) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cfg, err := validateConfig(cfg, filename, executable)
		if err != nil {
			return err
		}
		newData, newReadErr := os.ReadFile(filename)
		if (newReadErr != nil && !errors.Is(newReadErr, os.ErrNotExist)) ||
			(readErr == nil) != (newReadErr == nil) || string(oldData) != string(newData) {
			return errors.New("Indstillingerne blev ændret under opsætningen. Åbn de nyeste indstillinger, og prøv igen.")
		}
		return saveConfig(filename, cfg)
	}
	if cfg.TenantID == "00000000-0000-0000-0000-000000000000" {
		cfg.TenantID = ""
	}
	if credentialFilesMissing(dir) {
		logger.Print("Opretter et lokalt klientcertifikat til SharePoint.")
		if err := generateCertificate(dir); err != nil {
			return err
		}
	}
	cred, err := loadCredential(cfg, dir)
	if err != nil {
		return fmt.Errorf("læs det lokale klientcertifikat og nøglen: %w", err)
	}
	if previous, readErr := readConfig(filename); readErr == nil &&
		uuidPattern.MatchString(previous.TenantID) && uuidPattern.MatchString(previous.ClientID) {
		if previousSite, parseErr := url.Parse(previous.SiteURL); parseErr == nil && isSharePointURL(previousSite) &&
			!strings.EqualFold(previousSite.Host, link.site.Host) && cfg.TenantID == previous.TenantID {
			cfg.TenantID = ""
		}
		// Reusing an existing connection keeps repeated setup from creating a new Entra app.
		if services.verify(ctx, previous, link, dir, 1) == nil {
			copySetupConfig(&cfg, previous)
			logger.Print("Den eksisterende SharePoint-forbindelse er bekræftet.")
			return save(cfg)
		}
	}
	logger.Print("Starter Microsoft Graph PowerShell til administratorlogin.")
	admin, err := services.connect(ctx, cfg.TenantID, logger)
	if err != nil {
		return err
	}
	defer admin.close()
	if !uuidPattern.MatchString(admin.tenantID()) {
		return errors.New("Microsoft Graph returnerede ikke et gyldigt lejer-id.")
	}
	logger.Print("Kontrollerer SharePoint-webstedet, biblioteket og mappen.")
	target, err := findSetupTarget(ctx, admin, link)
	if err != nil {
		return err
	}
	logger.Printf("Opretter Entra-app til %s.", link.site.String())
	reg, err := createSharePointAccess(ctx, admin, target, cred)
	defer func() {
		if err != nil {
			if cleanupErr := reg.rollback(admin); cleanupErr != nil {
				// Include the exact app identifier if a tenant admin must finish cleanup.
				err = errors.Join(err, fmt.Errorf("automatisk oprydning mislykkedes for Entra-app %s: %w", reg.applicationID, cleanupErr))
			}
		}
	}()
	if err != nil {
		return err
	}
	cfg.SiteURL, cfg.DriveID, cfg.DocumentLibrary, cfg.DestinationFolder = link.site.String(), target.driveID, target.library, target.folder
	cfg.TenantID, cfg.ClientID = admin.tenantID(), reg.clientID
	logger.Print("Afprøver forbindelsen med arkivappens certifikat. Microsofts rettigheder kan tage lidt tid at træde i kraft.")
	if err := services.verify(ctx, cfg, link, dir, 10); err != nil {
		return fmt.Errorf("SharePoint-forbindelsen kunne ikke bekræftes: %w", err)
	}
	if err := save(cfg); err != nil {
		return err
	}
	logger.Print("SharePoint-forbindelsen er bekræftet, og indstillingerne er gemt.")
	return nil
}

func credentialFilesMissing(dir string) bool {
	_, certErr := os.Stat(filepath.Join(dir, certName))
	_, keyErr := os.Stat(filepath.Join(dir, keyName))
	return errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist)
}

func copySetupConfig(cfg *config, from config) {
	cfg.SiteURL, cfg.DriveID, cfg.DocumentLibrary, cfg.DestinationFolder = from.SiteURL, from.DriveID, from.DocumentLibrary, from.DestinationFolder
	cfg.TenantID, cfg.ClientID = from.TenantID, from.ClientID
}

func verifySetupConnection(ctx context.Context, cfg config, link setupLink, dir string, attempts int) error {
	site, err := url.Parse(cfg.SiteURL)
	if err != nil || !isSharePointURL(site) || !strings.EqualFold(site.Host, link.site.Host) ||
		!strings.EqualFold(strings.TrimRight(site.Path, "/"), link.site.Path) {
		return errors.New("Den gemte SharePoint-forbindelse bruger et andet websted.")
	}
	cred, err := loadCredential(cfg, dir)
	if err != nil {
		return err
	}
	graph := newGraph(cred)
	graph.client.Timeout = 8 * time.Second
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	return retrySetup(ctx, attempts, 5*time.Second, func() error {
		folder, err := graph.findFolder(ctx, cfg)
		if err != nil {
			return err
		}
		if !link.matches(folder.webURL) {
			return errors.New("Arkivappen fik adgang til en anden mappe end den valgte.")
		}
		return nil
	}, func(err error) bool { return !errors.Is(err, context.Canceled) })
}
