package webdavarchive

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClientProbeAndPut(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "alice" || password != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.EscapedPath()+" "+string(body))
		mu.Unlock()
		switch request.Method {
		case "MKCOL", http.MethodPut:
			writer.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		Endpoint: server.URL + "/dav", Username: "alice", Password: "secret", BasePath: "hideck/lab",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Put(context.Background(), Upload{
		RemotePath: "sms/2026/message name.json", Data: []byte("{}"), ContentType: "application/json",
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(requests, "\n")
	for _, want := range []string{
		"MKCOL /dav/hideck ", "MKCOL /dav/hideck/lab ",
		"PUT /dav/hideck/lab/.hideck-probe-", "DELETE /dav/hideck/lab/.hideck-probe-",
		"MKCOL /dav/hideck/lab/sms ", "MKCOL /dav/hideck/lab/sms/2026 ",
		"PUT /dav/hideck/lab/sms/2026/message%20name.json {}",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("requests missing %q:\n%s", want, joined)
		}
	}
}

func TestClientRejectsUnsafePathsAndRedactsPassword(t *testing.T) {
	if _, err := NewClient(ClientConfig{Endpoint: "ftp://example.test/dav"}); err == nil {
		t.Fatal("ftp endpoint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("storage unavailable do-not-log " + request.Header.Get("Authorization")))
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Username: "alice", Password: "do-not-log"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Put(context.Background(), Upload{RemotePath: "../escape"}); err == nil {
		t.Fatal("traversal path accepted")
	}
	err = client.Put(context.Background(), Upload{RemotePath: "safe.json", ContentType: "application/json"})
	if err == nil || !strings.Contains(err.Error(), "storage unavailable") ||
		strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "YWxpY2U6ZG8tbm90LWxvZw==") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientDoesNotTreatRedirectedPutAsSuccess(t *testing.T) {
	redirectTargetCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			redirectTargetCalled = true
			writer.WriteHeader(http.StatusOK)
			return
		}
		if request.Method == "MKCOL" {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.Redirect(writer, request, "/target", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Put(context.Background(), Upload{RemotePath: "message.json", Data: []byte("{}")}); err == nil {
		t.Fatal("redirected PUT reported success")
	}
	if redirectTargetCalled {
		t.Fatal("WebDAV client followed redirect")
	}
}
