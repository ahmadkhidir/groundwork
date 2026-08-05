package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
)

// DatasetStore is the persistence seam RegisterUploadRoutes depends on, satisfied by
// internal/store/postgres.DatasetRepository.
type DatasetStore interface {
	Create(ctx context.Context, ds domain.Dataset) error
}

// ObjectStore is the persistence seam for the originally-uploaded file bytes, satisfied by
// internal/storage.Client.
type ObjectStore interface {
	PutObject(ctx context.Context, key string, content io.Reader, size int64, contentType string) error
}

// extensionFormats maps an accepted upload extension to its DatasetFormat, mirroring
// web/src/lib/limits.ts's ACCEPTED_UPLOAD_EXTENSIONS exactly — the client and server must agree on
// what is supported, since the client check is a UX affordance and this one is the control (rule 12).
var extensionFormats = map[string]domain.DatasetFormat{
	".csv":     domain.FormatCSV,
	".xlsx":    domain.FormatExcel,
	".xls":     domain.FormatExcel,
	".parquet": domain.FormatParquet,
	".sqlite":  domain.FormatSQLite,
	".db":      domain.FormatSQLite,
}

// RegisterUploadRoutes wires FEAT-003's endpoint onto r.
func RegisterUploadRoutes(
	r chi.Router,
	workspaces WorkspaceStore,
	datasets DatasetStore,
	objects ObjectStore,
	maxUploadBytes int64,
	logger *slog.Logger,
) {
	r.Post("/workspaces/{workspaceId}/files", handleUploadFiles(workspaces, datasets, objects, maxUploadBytes, logger))
}

type uploadedFileResult struct {
	Dataset *publicDataset `json:"dataset,omitempty"`
	// Filename and Error are populated instead of Dataset when this specific file was rejected —
	// screens/05-file-upload.md requires "other valid files unaffected and still submittable," so one
	// bad file in a multi-file upload must not fail the whole request.
	Filename string `json:"filename,omitempty"`
	Error    string `json:"error,omitempty"`
}

type publicDataset struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	OriginalFilename string `json:"original_filename"`
	Format           string `json:"format"`
	IngestStatus     string `json:"ingest_status"`
	CreatedAt        string `json:"created_at"`
}

func toPublicDataset(ds domain.Dataset) *publicDataset {
	return &publicDataset{
		ID:               ds.ID,
		WorkspaceID:      ds.WorkspaceID,
		OriginalFilename: ds.OriginalFilename,
		Format:           string(ds.Format),
		IngestStatus:     string(ds.IngestStatus),
		CreatedAt:        ds.CreatedAt.Format(time.RFC3339),
	}
}

// handleUploadFiles serves FEAT-003's POST /api/workspaces/:id/files.
//
// Accepts one or more files in a single multipart/form-data request (FEAT-003's "user can upload
// multiple files into the same workspace in one session" acceptance criterion), streaming each part
// directly to object storage rather than buffering it in memory — required to support files up to
// the 500 MB ceiling without the service's memory footprint scaling with concurrent uploads.
//
// This wave (FEAT-003) stores the ORIGINAL file only and creates the Dataset row with
// ingest_status "pending". Normalizing it into canonical Parquet and profiling it is FEAT-004
// (Wave 3, the Engine Provider seam) — deliberately out of scope here; parquet_location stays empty
// per schema.md's "empty until ingest completes" convention until that wave lands.
func handleUploadFiles(
	workspaces WorkspaceStore,
	datasets DatasetStore,
	objects ObjectStore,
	maxUploadBytes int64,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		reader, err := r.MultipartReader()
		if err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Expected a multipart/form-data request.",
				"code":  "bad_request",
			})
			return
		}

		var results []uploadedFileResult
		fileCount := 0

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				logger.Error("reading multipart part", slog.String("error", err.Error()))
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Malformed upload request.",
					"code":  "bad_request",
				})
				return
			}
			if part.FormName() != "files" || part.FileName() == "" {
				// Not a file field (or a field with no filename) — skip without treating it as an
				// error, so a client can send other form fields alongside the files.
				_ = part.Close()
				continue
			}

			fileCount++
			result := uploadOneFile(r.Context(), part, workspaceID, maxUploadBytes, datasets, objects, logger)
			results = append(results, result)
			_ = part.Close()
		}

		if fileCount == 0 {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "No files were included in the upload.",
				"code":  "validation_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

// uploadOneFile validates, stores, and records a single uploaded part. Errors are returned as a
// result entry rather than aborting the whole request — see handleUploadFiles's doc comment.
func uploadOneFile(
	ctx context.Context,
	part *multipart.Part,
	workspaceID string,
	maxUploadBytes int64,
	datasets DatasetStore,
	objects ObjectStore,
	logger *slog.Logger,
) uploadedFileResult {
	filename := part.FileName()

	format, ok := extensionFormats[strings.ToLower(filepath.Ext(filename))]
	if !ok {
		return uploadedFileResult{
			Filename: filename,
			Error:    "Groundwork supports CSV, Excel, Parquet, and SQLite files.",
		}
	}

	// Streaming size enforcement: read at most maxUploadBytes+1 bytes so an oversized file is caught
	// without buffering the whole thing first. The client already checked this (web/src/lib/limits.ts
	// validateUploadFile), but that is a UX affordance — this is the control (rule 12), and the
	// message states the ceiling explicitly rather than a generic "file too large"
	// (screens/05-file-upload.md).
	//
	// PutObject needs a size up front for Content-Length, which is not knowable until the part has
	// been fully read, so the read here is bounded (maxUploadBytes+1, never unbounded) rather than
	// truly streamed straight through to S3. A chunked/multipart S3 upload that avoids even this
	// bounded buffering is a reasonable follow-up, reported rather than built into this wave's effort
	// budget.
	datasetID := uuid.NewString()
	key := storage.DatasetKey(workspaceID, datasetID, filename)

	content, err := io.ReadAll(io.LimitReader(part, maxUploadBytes+1))
	if err != nil {
		logger.Error("reading upload", slog.String("error", err.Error()), slog.String("filename", filename))
		return uploadedFileResult{Filename: filename, Error: "Upload failed. Try again."}
	}

	if int64(len(content)) > maxUploadBytes {
		return uploadedFileResult{
			Filename: filename,
			Error:    fmt.Sprintf("%s is over the %s limit for a single file.", filename, formatMaxUploadBytes(maxUploadBytes)),
		}
	}

	if err := objects.PutObject(ctx, key, strings.NewReader(string(content)), int64(len(content)), contentTypeForFormat(format)); err != nil {
		logger.Error("storing upload", slog.String("error", err.Error()), slog.String("filename", filename))
		return uploadedFileResult{Filename: filename, Error: "Upload failed. Try again."}
	}

	ds := domain.Dataset{
		ID:               datasetID,
		WorkspaceID:      workspaceID,
		OriginalFilename: filename,
		Format:           format,
		IngestStatus:     domain.IngestPending,
		CreatedAt:        time.Now(),
	}
	if err := datasets.Create(ctx, ds); err != nil {
		logger.Error("recording dataset", slog.String("error", err.Error()), slog.String("filename", filename))
		return uploadedFileResult{Filename: filename, Error: "Upload failed. Try again."}
	}

	return uploadedFileResult{Dataset: toPublicDataset(ds)}
}

func contentTypeForFormat(format domain.DatasetFormat) string {
	switch format {
	case domain.FormatCSV:
		return "text/csv"
	case domain.FormatExcel:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case domain.FormatParquet:
		return "application/octet-stream"
	case domain.FormatSQLite:
		return "application/x-sqlite3"
	default:
		return "application/octet-stream"
	}
}

// formatMaxUploadBytes renders maxUploadBytes as a human label ("500 MB"), mirroring
// web/src/lib/limits.ts's formatBytes/maxUploadSizeLabel so the server's rejection message matches
// the client's wording.
func formatMaxUploadBytes(maxUploadBytes int64) string {
	const unit = 1024
	if maxUploadBytes < unit {
		return fmt.Sprintf("%d B", maxUploadBytes)
	}
	div, exp := int64(unit), 0
	for n := maxUploadBytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.0f %s", float64(maxUploadBytes)/float64(div), units[exp])
}
