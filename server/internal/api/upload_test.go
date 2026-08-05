package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeDatasetStore is an in-memory DatasetStore.
type fakeDatasetStore struct {
	created []domain.Dataset
}

func newFakeDatasetStore() *fakeDatasetStore {
	return &fakeDatasetStore{}
}

func (f *fakeDatasetStore) Create(ctx context.Context, ds domain.Dataset) error {
	f.created = append(f.created, ds)
	return nil
}

// fakeObjectStore is an in-memory ObjectStore, recording every write instead of touching S3/MinIO.
type fakeObjectStore struct {
	puts map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{puts: make(map[string][]byte)}
}

func (f *fakeObjectStore) PutObject(ctx context.Context, key string, content io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.puts[key] = data
	return nil
}

// multipartUploadRequest builds a multipart/form-data upload request with one "files" field per
// (filename, content) pair, matching what web/src/components/upload sends.
func multipartUploadRequest(t *testing.T, path string, files map[string][]byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for filename, content := range files {
		part, err := writer.CreateFormFile("files", filename)
		if err != nil {
			t.Fatalf("creating form file: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("writing form file content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestUploadFileSucceeds(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := multipartUploadRequest(t, "/v1/workspaces/ws-1/files", map[string][]byte{
		"orders.csv": []byte("id,total\n1,42.00\n"),
	})
	attachSessionCookie(t, req, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Results []struct {
			Dataset *struct {
				OriginalFilename string `json:"original_filename"`
				Format           string `json:"format"`
				IngestStatus     string `json:"ingest_status"`
			} `json:"dataset"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].Dataset == nil {
		t.Fatalf("Results = %+v, want one successful dataset", body.Results)
	}
	got := body.Results[0].Dataset
	if got.OriginalFilename != "orders.csv" {
		t.Errorf("OriginalFilename = %q, want orders.csv", got.OriginalFilename)
	}
	if got.Format != string(domain.FormatCSV) {
		t.Errorf("Format = %q, want %q", got.Format, domain.FormatCSV)
	}
	if got.IngestStatus != string(domain.IngestPending) {
		t.Errorf("IngestStatus = %q, want %q (FEAT-004 normalization is a later wave)", got.IngestStatus, domain.IngestPending)
	}
	if len(deps.datasets.created) != 1 {
		t.Errorf("created dataset count = %d, want 1", len(deps.datasets.created))
	}
	if len(deps.objects.puts) != 1 {
		t.Errorf("stored object count = %d, want 1", len(deps.objects.puts))
	}
}

func TestUploadRejectsUnsupportedFileType(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := multipartUploadRequest(t, "/v1/workspaces/ws-1/files", map[string][]byte{
		"resume.docx": []byte("not a dataset"),
	})
	attachSessionCookie(t, req, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (per-file errors, not a request-level failure)", rec.Code, http.StatusOK)
	}

	var body struct {
		Results []struct {
			Dataset *json.RawMessage `json:"dataset"`
			Error   string           `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].Dataset != nil {
		t.Fatalf("Results = %+v, want one rejected file", body.Results)
	}
	// screens/05-file-upload.md: rejection must be specific, not a bare "unsupported".
	if body.Results[0].Error == "" {
		t.Error("expected a specific rejection message")
	}
}

func TestUploadRejectsOversizedFile(t *testing.T) {
	// The default test server's ceiling is config.MaxUploadBytesDefault (500 MB); this test wires a
	// server with a tiny limit instead so it can trigger the oversized path without allocating a
	// 500MB+ byte slice in the test process.
	tinyServer, tinyDeps := newTestServerWithTinyUploadLimit(t)
	tinyDeps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := multipartUploadRequest(t, "/v1/workspaces/ws-1/files", map[string][]byte{
		"big.csv": bytes.Repeat([]byte("a"), 100),
	})
	attachSessionCookie(t, req, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	tinyServer.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Results []struct {
			Dataset *json.RawMessage `json:"dataset"`
			Error   string           `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].Dataset != nil {
		t.Fatalf("Results = %+v, want the oversized file rejected", body.Results)
	}
	// rule 12: the rejection message must state the ceiling explicitly, never a generic "too large".
	if !bytes.Contains([]byte(body.Results[0].Error), []byte("B limit")) {
		t.Errorf("Error = %q, want it to state the size limit explicitly", body.Results[0].Error)
	}
}

func TestUploadMultipleFilesMixedValidity(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := multipartUploadRequest(t, "/v1/workspaces/ws-1/files", map[string][]byte{
		"good.csv": []byte("id\n1\n"),
		"bad.docx": []byte("not supported"),
	})
	attachSessionCookie(t, req, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Results []struct {
			Dataset *json.RawMessage `json:"dataset"`
			Error   string           `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Results) != 2 {
		t.Fatalf("Results = %+v, want 2 entries (one valid, one rejected)", body.Results)
	}
	var successCount, errorCount int
	for _, r := range body.Results {
		if r.Dataset != nil {
			successCount++
		} else {
			errorCount++
		}
	}
	if successCount != 1 || errorCount != 1 {
		t.Errorf("successCount=%d errorCount=%d, want 1 and 1 — a bad file must not block the good one", successCount, errorCount)
	}
}

func TestUploadRequiresWorkspaceOwnership(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}

	req := multipartUploadRequest(t, "/v1/workspaces/ws-1/files", map[string][]byte{
		"orders.csv": []byte("id\n1\n"),
	})
	attachSessionCookie(t, req, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
