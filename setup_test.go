package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testSite = "https://firma.sharepoint.com/sites/Drift"
const testFolderPath = "/sites/Drift/Shared Documents/Eksport #1"
const testTenant = "00000000-0000-0000-0000-000000000001"
const testClient = "00000000-0000-0000-0000-000000000002"

func TestSetupFolderLinks(t *testing.T) {
	base := "https://firma.sharepoint.com"
	for _, test := range []struct{ link, path string }{
		{testSite + "/Shared%20Documents/Eksport%20%231", testFolderPath},
		{testSite + "/Shared%20Documents/Forms/AllItems.aspx?id=%2Fsites%2FDrift%2FShared%20Documents%2FEksport%20%231", testFolderPath},
		{base + "/:f:/r/sites/Drift/Shared%20Documents/Eksport%20%231?web=1&e=sharing-value", testFolderPath},
		{testSite + "/Shared%20Documents/Forms/AllItems.aspx", "/sites/Drift/Shared Documents"},
		{testSite + "/Shared%20Documents/Forms/AllItems.aspx?RootFolder=%2Fsites%2FDrift%2FShared%20Documents%2FEksport%20%231", testFolderPath},
	} {
		parsed, err := parseSetupLink(test.link)
		if err != nil || parsed.site.String() != testSite || parsed.path != test.path {
			t.Fatalf("parseSetupLink(%q) = %#v, %v", test.link, parsed, err)
		}
		if parsed.matches(base + "/sites/Drift/Shared%20Documents/Other") {
			t.Fatal("an unrelated folder was accepted")
		}
	}
	for _, link := range []string{
		"http://firma.sharepoint.com/Shared%20Documents",
		"https://firma.sharepoint.com.evil.example/sites/Drift/Docs/F",
		"https://login@firma.sharepoint.com/sites/Drift/Docs/F",
		testSite,
		testSite + "/Docs/%2e%2e/Other",
		base + "/:f:/s/Drift/opaque",
		base + "/personal/User/Docs/Folder",
	} {
		if _, err := parseSetupLink(link); err == nil {
			t.Fatalf("accepted unsupported link %q", link)
		}
	}
}

type setupStep struct {
	method, path, reply string
	inspect             func(any)
}

type mockSetupSession struct {
	t      *testing.T
	steps  []setupStep
	closed bool
}

func (m *mockSetupSession) tenantID() string { return testTenant }
func (m *mockSetupSession) close()           { m.closed = true }
func (m *mockSetupSession) request(_ context.Context, method, path string, payload, target any) error {
	m.t.Helper()
	if len(m.steps) == 0 || m.steps[0].method != method || m.steps[0].path != path {
		m.t.Fatalf("unexpected Microsoft Graph request: %s %s", method, path)
	}
	step := m.steps[0]
	m.steps = m.steps[1:]
	if step.inspect != nil {
		step.inspect(payload)
	}
	if target != nil {
		if err := json.Unmarshal([]byte(step.reply), target); err != nil {
			m.t.Fatal(err)
		}
	}
	return nil
}

func TestSetupSavesVerifiedConnectionAndRollsBackFailures(t *testing.T) {
	for _, verifyFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("verify-fails-%t", verifyFails), func(t *testing.T) {
			dir, source := t.TempDir(), t.TempDir()
			configFile := filepath.Join(dir, "config.json")
			old := defaultConfig()
			old.LocalFolder = source
			old.IntervalMinutes = 10
			if err := saveConfig(configFile, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			linkURL := testSite + "/Shared%20Documents/Eksport%20%231"
			link, err := parseSetupLink(linkURL)
			if err != nil {
				t.Fatal(err)
			}
			const appID = "00000000-0000-0000-0000-000000000003"
			const principalID = "00000000-0000-0000-0000-000000000004"
			const graphID = "00000000-0000-0000-0000-000000000005"
			const roleID = "00000000-0000-0000-0000-000000000006"
			roleReply := fmt.Sprintf(`{"id":%q,"appRoles":[{"id":%q,"value":"Sites.Selected","isEnabled":true,"allowedMemberTypes":["Application"]}]}`, graphID, roleID)
			session := &mockSetupSession{t: t, steps: []setupStep{
				{"GET", "/sites/firma.sharepoint.com:/sites/Drift?$select=id,webUrl", fmt.Sprintf(`{"id":"site","webUrl":%q}`, testSite), nil},
				{"GET", "/sites/site/drives?$select=id,name,webUrl", fmt.Sprintf(`{"value":[{"id":"doc","name":"Dokumenter","webUrl":%q}]}`, testSite+"/Shared%20Documents"), nil},
				{"GET", "/drives/doc/root:/Eksport%20%231?$select=id,folder,webUrl", fmt.Sprintf(`{"id":"folder","folder":{},"webUrl":%q}`, linkURL), nil},
				{"GET", "/servicePrincipals(appId='" + microsoftGraphAppID + "')?$select=id,appRoles", roleReply, nil},
				{"POST", "/applications", fmt.Sprintf(`{"id":%q,"appId":%q}`, appID, testClient), func(payload any) {
					cert, err := os.ReadFile(filepath.Join(dir, certName))
					if err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(payload)
					if err != nil || !strings.Contains(string(data), base64.StdEncoding.EncodeToString(cert)) ||
						strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), "Sites.ReadWrite.All") {
						t.Fatal("unexpected application registration payload")
					}
				}},
				{"POST", "/servicePrincipals", fmt.Sprintf(`{"id":%q}`, principalID), nil},
				{"POST", "/servicePrincipals/" + graphID + "/appRoleAssignedTo", "", func(payload any) {
					if !reflect.DeepEqual(payload, map[string]string{"principalId": principalID, "resourceId": graphID, "appRoleId": roleID}) {
						t.Fatal("unexpected Graph role assignment")
					}
				}},
				{"POST", "/sites/site/permissions", `{"id":"permission"}`, nil},
			}}
			if verifyFails {
				session.steps = append(session.steps,
					setupStep{"DELETE", "/sites/site/permissions/permission", "", nil},
					setupStep{"DELETE", "/servicePrincipals/" + principalID, "", nil},
					setupStep{"DELETE", "/applications/" + appID, "", nil})
			}
			services := setupServices{
				connect: func(context.Context, string, *log.Logger) (setupAdminSession, error) { return session, nil },
				verify: func(_ context.Context, cfg config, _ setupLink, _ string, _ int) error {
					if cfg.TenantID != testTenant || cfg.ClientID != testClient ||
						cfg.DriveID != "doc" || cfg.DestinationFolder != "Eksport #1" {
						t.Fatalf("bad verification config: %#v", cfg)
					}
					if verifyFails {
						return errors.New("permission did not propagate")
					}
					return nil
				},
			}
			err = services.configure(t.Context(), old, link, configFile, exe)
			if (err != nil) != verifyFails || len(session.steps) != 0 || !session.closed {
				t.Fatalf("error %v, uncalled steps %d, closed %t", err, len(session.steps), session.closed)
			}
			if verifyFails {
				after, err := os.ReadFile(configFile)
				if err != nil || string(after) != string(before) {
					t.Fatal("unverified configuration overwrote saved settings")
				}
			} else {
				saved, err := readConfig(configFile)
				if err != nil || saved.ClientID != testClient || saved.IntervalMinutes != 10 ||
					saved.LocalFolder != source || saved.DeleteLocal {
					t.Fatalf("saved configuration changed local settings: %#v, %v", saved, err)
				}
			}
		})
	}
}
