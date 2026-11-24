package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGraphUploadDoesNotReplaceExistingFiles(t *testing.T) {
	const name = "A #1.csv"
	for _, size := range []int{0, 17, fragmentSize + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := bytes.Repeat([]byte("a"), size)
			graph := newGraph(&credential{token: "graph-token", expires: time.Now().Add(time.Hour)})
			folder := &graphFolder{graph: graph, folderURL: graphBase + "/drives/d/items/f"}
			var bytesSent int
			graph.client.Transport = testTransport(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var code int
				var response string
				if req.URL.Host == "graph.microsoft.com" {
					if req.Header.Get("Authorization") != "Bearer graph-token" ||
						!strings.Contains(req.URL.EscapedPath(), "A%20%231.csv") {
						t.Fatalf("incorrect path or authorization: %s", req.URL.EscapedPath())
					}
					if size <= simpleUploadLimit {
						if req.Method != http.MethodPut || req.URL.Query().Get("@microsoft.graph.conflictBehavior") != "fail" {
							t.Fatal("simple upload must fail on name conflict")
						}
						body, err := io.ReadAll(req.Body)
						if err != nil || !bytes.Equal(body, data) {
							t.Fatal("simple upload did not send the correct data")
						}
						bytesSent += len(body)
						code, response = http.StatusCreated, `{"id":"done"}`
					} else {
						body, err := io.ReadAll(req.Body)
						if err != nil || req.Method != http.MethodPost || !bytes.Contains(body, []byte(`"@microsoft.graph.conflictBehavior":"fail"`)) {
							t.Fatal("upload session must fail on name conflict")
						}
						code, response = http.StatusOK, `{"uploadUrl":"https://upload.example/session"}`
					}
				} else {
					if req.URL.Host != "upload.example" || req.Method != http.MethodPut || req.Header.Get("Authorization") != "" {
						t.Fatal("upload fragments must use the signed URL without an authorization header")
					}
					length := min(fragmentSize, size-bytesSent)
					expected := fmt.Sprintf("bytes %d-%d/%d", bytesSent, bytesSent+length-1, size)
					body, err := io.ReadAll(req.Body)
					if err != nil || req.Header.Get("Content-Range") != expected || !bytes.Equal(body, data[bytesSent:bytesSent+length]) {
						t.Fatal("incorrect upload fragment")
					}
					bytesSent += length
					if bytesSent == size {
						code, response = http.StatusCreated, `{"id":"done"}`
					} else {
						code, response = http.StatusAccepted, `{}`
					}
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			item, err := folder.upload(t.Context(), name, bytes.NewReader(data), int64(size))
			if err != nil || item.ID != "done" || bytesSent != size {
				t.Fatalf("upload result: id=%q, bytes=%d, err=%v", item.ID, bytesSent, err)
			}
		})
	}
}
