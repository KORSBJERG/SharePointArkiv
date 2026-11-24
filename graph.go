package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const graphBase = "https://graph.microsoft.com/v1.0"

// 10 MiB is a multiple of the 320 KiB fragment size required by Graph.
const fragmentSize = 10 * 1024 * 1024
const simpleUploadLimit = 4 * 1024 * 1024

type graph struct {
	credential *credential
	client     *http.Client
}

type graphError struct {
	status    int
	code      string
	requestID string
}

func (e *graphError) Error() string {
	return fmt.Sprintf("SharePoint svarede HTTP %d (%s, request-id: %s)", e.status, e.code, e.requestID)
}

func hasStatus(err error, status int) bool {
	var response *graphError
	return errors.As(err, &response) && response.status == status
}

type driveItem struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	ETag   string    `json:"eTag"`
	WebURL string    `json:"webUrl"`
	Folder *struct{} `json:"folder"`
	File   *struct{} `json:"file"`
}

type graphFolder struct {
	graph     *graph
	itemsURL  string
	folderURL string
	webURL    string
}

func newGraph(credential *credential) *graph {
	return &graph{
		credential: credential,
		client: &http.Client{
			Timeout: 30 * time.Minute,
			CheckRedirect: func(req *http.Request, previous []*http.Request) error {
				if len(previous) == 1 && previous[0].URL.Host == "graph.microsoft.com" &&
					strings.HasSuffix(previous[0].URL.Path, "/content") &&
					previous[0].Method == http.MethodGet && req.URL.Scheme == "https" && req.URL.User == nil {
					req.Header.Del("Authorization")
					req.Header.Del("Referer")
					return nil
				}
				return http.ErrUseLastResponse
			},
		},
	}
}

func (g *graph) findFolder(ctx context.Context, cfg config) (*graphFolder, error) {
	driveID := cfg.DriveID
	if driveID == "" {
		var err error
		driveID, err = g.findDriveID(ctx, cfg)
		if err != nil {
			return nil, err
		}
	}

	driveURL := graphBase + "/drives/" + url.PathEscape(driveID)
	folderURL := graphBase + driveRootPath(driveID, cfg.DestinationFolder)
	var item driveItem
	if err := g.json(ctx, http.MethodGet, folderURL+"?$select=id,folder,webUrl", nil, &item); err != nil {
		return nil, fmt.Errorf("find destinationsmappen %q: %w", cfg.DestinationFolder, err)
	}
	if item.ID == "" || item.Folder == nil {
		return nil, errors.New("destinationen skal være en eksisterende SharePoint-mappe")
	}
	site, _ := url.Parse(cfg.SiteURL)
	destination, err := url.Parse(item.WebURL)
	if err != nil || destination.Scheme != "https" || !strings.EqualFold(destination.Host, site.Host) ||
		!strings.HasPrefix(strings.ToLower(destination.Path), strings.ToLower(strings.TrimRight(site.Path, "/"))+"/") {
		return nil, errors.New("SharePoint-mappen ligger uden for det konfigurerede websted")
	}

	return &graphFolder{
		graph:     g,
		itemsURL:  driveURL + "/items/",
		folderURL: driveURL + "/items/" + url.PathEscape(item.ID),
		webURL:    item.WebURL,
	}, nil
}

func graphSitePath(site *url.URL) string {
	path := strings.TrimRight(site.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	return "/sites/" + site.Hostname() + ":" + path
}

func driveRootPath(driveID, folder string) string {
	root := "/drives/" + url.PathEscape(driveID) + "/root"
	if folder == "" {
		return root
	}
	parts := strings.Split(folder, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return root + ":/" + strings.Join(parts, "/")
}

func (g *graph) findDriveID(ctx context.Context, cfg config) (string, error) {
	siteURL, err := url.Parse(cfg.SiteURL)
	if err != nil {
		return "", err
	}
	var site struct{ ID string }
	if err := g.json(ctx, http.MethodGet, graphBase+graphSitePath(siteURL)+"?$select=id", nil, &site); err != nil {
		return "", fmt.Errorf("find websted: %w", err)
	}
	if site.ID == "" {
		return "", errors.New("webstedets id mangler i svaret fra SharePoint")
	}

	var driveID string
	next := graphBase + "/sites/" + url.PathEscape(site.ID) + "/drives?$select=id,name"
	for next != "" {
		var result struct {
			Value []struct{ ID, Name string }
			Next  string `json:"@odata.nextLink"`
		}
		if err := g.json(ctx, http.MethodGet, next, nil, &result); err != nil {
			return "", fmt.Errorf("find dokumentbibliotek: %w", err)
		}
		for _, drive := range result.Value {
			if strings.EqualFold(drive.Name, cfg.DocumentLibrary) {
				if driveID != "" && driveID != drive.ID {
					return "", fmt.Errorf("flere biblioteker hedder %q", cfg.DocumentLibrary)
				}
				driveID = drive.ID
			}
		}
		next = result.Next
	}
	if driveID == "" {
		return "", fmt.Errorf("dokumentbiblioteket %q findes ikke på webstedet", cfg.DocumentLibrary)
	}

	return driveID, nil
}

func (g *graph) request(ctx context.Context, method, address, contentType string, body io.Reader) (*http.Response, error) {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "graph.microsoft.com" ||
		endpoint.User != nil || !strings.HasPrefix(endpoint.Path, "/v1.0/") {
		return nil, errors.New("ugyldig Microsoft Graph-adresse")
	}
	token, err := g.credential.accessToken(ctx, g.client)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		// A redirect or transport error can contain a signed download URL.
		return nil, fmt.Errorf("Microsoft Graph: %w", transportError(err))
	}
	if resp.StatusCode == http.StatusUnauthorized {
		g.credential.token = ""
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readGraphError(resp)
	}
	return resp, nil
}

func transportError(err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		if requestErr.Timeout() {
			return errors.New("netværksforbindelsen fik timeout")
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
	}
	return errors.New("netværksforbindelsen blev afbrudt")
}

func readGraphError(resp *http.Response) error {
	var result struct {
		Error struct{ Code string }
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result)
	return &graphError{status: resp.StatusCode, code: result.Error.Code, requestID: resp.Header.Get("request-id")}
}

func (g *graph) json(ctx context.Context, method, address string, payload any, target any) error {
	var reader io.Reader
	var contentType string
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		contentType = "application/json"
	}
	resp, err := g.request(ctx, method, address, contentType, reader)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(target)
}

func (f *graphFolder) lookup(ctx context.Context, name string) (driveItem, error) {
	var item driveItem
	err := f.graph.json(ctx, http.MethodGet, f.filePath(name)+"?$select=id,name,size,eTag,file", nil, &item)
	return item, err
}

func (f *graphFolder) filePath(name string) string {
	return f.folderURL + ":/" + url.PathEscape(name)
}

func (f *graphFolder) download(ctx context.Context, id string) (io.ReadCloser, error) {
	resp, err := f.graph.request(ctx, http.MethodGet, f.itemsURL+url.PathEscape(id)+"/content", "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download af SharePoint-filen svarede HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (f *graphFolder) upload(ctx context.Context, name string, source io.ReaderAt, size int64) (driveItem, error) {
	if size <= simpleUploadLimit {
		data := make([]byte, int(size))
		if _, err := io.ReadFull(io.NewSectionReader(source, 0, size), data); err != nil {
			return driveItem{}, err
		}
		resp, err := f.graph.request(ctx, http.MethodPut,
			f.filePath(name)+":/content?%40microsoft.graph.conflictBehavior=fail",
			"application/octet-stream", bytes.NewReader(data))
		if err != nil {
			return driveItem{}, err
		}
		defer resp.Body.Close()
		var item driveItem
		err = json.NewDecoder(resp.Body).Decode(&item)
		return item, err
	}

	var session struct{ UploadURL string }
	payload := map[string]any{"item": map[string]string{"@microsoft.graph.conflictBehavior": "fail"}}
	if err := f.graph.json(ctx, http.MethodPost, f.filePath(name)+":/createUploadSession", payload, &session); err != nil {
		return driveItem{}, err
	}
	endpoint, err := url.Parse(session.UploadURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		return driveItem{}, errors.New("SharePoint returnerede en ugyldig uploadsession")
	}

	for offset := int64(0); offset < size; {
		length := min(int64(fragmentSize), size-offset)
		upload, finished, err := f.uploadFragment(ctx, session.UploadURL, source, offset, length, size)
		if err != nil {
			return driveItem{}, err
		}
		offset += length
		if finished {
			if offset != size {
				return driveItem{}, errors.New("SharePoint afsluttede uploaden før sidste fragment")
			}
			return upload, nil
		}
	}
	return driveItem{}, errors.New("SharePoint bekræftede ikke den færdige upload")
}

func (f *graphFolder) uploadFragment(ctx context.Context, address string, source io.ReaderAt, offset, length, size int64) (driveItem, bool, error) {
	var item driveItem
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, address, io.NewSectionReader(source, offset, length))
	if err != nil {
		return item, false, errors.New("kunne ikke åbne uploadsessionen")
	}
	req.ContentLength = length
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size))
	req.Header.Set("Content-Type", "application/octet-stream")
	// The upload URL is preauthenticated. Never send the Graph bearer token to it.
	resp, err := f.graph.client.Do(req)
	if err != nil {
		return item, false, transportError(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		_, err := io.Copy(io.Discard, resp.Body)
		return item, false, err
	case http.StatusOK, http.StatusCreated:
		err := json.NewDecoder(resp.Body).Decode(&item)
		return item, err == nil, err
	default:
		return item, false, readGraphError(resp)
	}
}
