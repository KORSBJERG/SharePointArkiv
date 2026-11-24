package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

//go:embed setup_graph.ps1
var setupPowerShell string

type setupReply struct {
	Kind, Stage, Tenant, Body, Code string
	Status                          int
}

type powerShellGraph struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Scanner
	tenant  string
}

func connectPowerShellGraph(ctx context.Context, tenant string, logger *log.Logger) (setupAdminSession, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("find Windows PowerShell: %w", err)
	}
	chars := utf16.Encode([]rune(setupPowerShell))
	encoded := make([]byte, len(chars)*2)
	for i, ch := range chars {
		binary.LittleEndian.PutUint16(encoded[i*2:], ch)
	}
	ps := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(ps, "-NoProfile", "-NoLogo", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.Env = append(cmd.Environ(), "PEAKNET_SETUP_TENANT="+tenant)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, fmt.Errorf("start Windows PowerShell: %w", err)
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 8192), 4*1024*1024)
	graph := &powerShellGraph{command: cmd, input: input, output: scanner}

	// Cancel the sign-in process before any Graph changes have been made.
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	type signIn struct {
		reply setupReply
		err   error
	}
	ready := make(chan signIn, 1)
	go func() {
		for {
			reply, err := graph.read()
			if err != nil || reply.Kind != "progress" {
				ready <- signIn{reply, err}
				return
			}
			if reply.Stage == "module" {
				logger.Print("Installerer Microsoft.Graph.Authentication til den aktuelle Windows-bruger.")
			} else {
				logger.Print("Venter på administratorlogin i Microsofts loginvindue.")
			}
		}
	}()
	select {
	case <-ctx.Done():
		cmd.Process.Kill()
		<-ready
		graph.close()
		return nil, ctx.Err()
	case result := <-ready:
		if result.err != nil {
			graph.close()
			return nil, fmt.Errorf("Microsoft Graph PowerShell kunne ikke starte: %w", result.err)
		}
		if result.reply.Kind != "ready" {
			graph.close()
			if result.reply.Stage == "module" {
				return nil, errors.New("Microsoft Graph PowerShell kunne ikke installeres eller indlæses. Se den manuelle opsætning i vejledningen.")
			}
			if result.reply.Code != "" {
				return nil, fmt.Errorf("Microsoft-login blev afvist (%s). Kontrollér rollen og Microsoft Graph PowerShell-samtykket", result.reply.Code)
			}
			return nil, errors.New("Microsoft-login blev afbrudt, eller de nødvendige rettigheder blev afvist.")
		}
		graph.tenant = result.reply.Tenant
		return graph, nil
	}
}

func (graph *powerShellGraph) tenantID() string { return graph.tenant }

func (graph *powerShellGraph) close() {
	graph.input.Close()
	// The child uses a process-only Graph session and exits when stdin closes.
	finished := make(chan struct{})
	go func() {
		graph.command.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		graph.command.Process.Kill()
		<-finished
	}
}

func (graph *powerShellGraph) read() (setupReply, error) {
	for graph.output.Scan() {
		line, found := strings.CutPrefix(graph.output.Text(), "PEAKNET_SETUP ")
		if !found {
			continue
		}
		var result setupReply
		err := json.Unmarshal([]byte(line), &result)
		return result, err
	}
	if err := graph.output.Err(); err != nil {
		return setupReply{}, err
	}
	return setupReply{}, io.ErrUnexpectedEOF
}

func (graph *powerShellGraph) request(ctx context.Context, method, path string, payload, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := setupGraphPath(path)
	if err != nil {
		return err
	}
	request := map[string]string{"method": method, "path": path}
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		request["body"] = string(data)
	}
	if err := json.NewEncoder(graph.input).Encode(request); err != nil {
		return errors.New("forbindelsen til Microsoft Graph PowerShell blev afbrudt")
	}
	// Let the in-flight request return so a cancellation cannot lose the ID of
	// a resource the wizard must roll back. The Graph session bounds each request.
	reply, err := graph.read()
	if err != nil || reply.Kind != "response" {
		return errors.New("Microsoft Graph PowerShell kunne ikke gennemføre forespørgslen")
	}
	if reply.Status < 200 || reply.Status >= 300 {
		return &graphError{status: reply.Status, code: reply.Code}
	}
	if target != nil {
		if err := json.Unmarshal([]byte(reply.Body), target); err != nil {
			return errors.New("Microsoft Graph returnerede et uventet svar under opsætningen")
		}
	}
	return nil
}

func setupGraphPath(path string) (string, error) {
	endpoint, err := url.Parse(path)
	if err != nil || endpoint.User != nil || endpoint.Fragment != "" || !strings.HasPrefix(endpoint.Path, "/") {
		return "", errors.New("ugyldig Microsoft Graph-adresse under opsætningen")
	}
	if endpoint.IsAbs() || endpoint.Host != "" {
		if endpoint.Scheme != "https" || endpoint.Host != "graph.microsoft.com" ||
			!strings.HasPrefix(endpoint.EscapedPath(), "/v1.0/") {
			return "", errors.New("ugyldig Microsoft Graph-adresse under opsætningen")
		}
		path = strings.TrimPrefix(endpoint.RequestURI(), "/v1.0")
	}
	return path, nil
}
