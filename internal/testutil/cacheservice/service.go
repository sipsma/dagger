// Package cacheservice is the in-repo test implementation of the /v1 cache
// service protocol (service design §12 rung 2): filesystem storage, a
// static bearer token, and the same endpoint semantics as the real
// dagger.io handlers — the protocol's reference implementation. It is
// deliberately dumb: one process, one org, a directory tree, no Postgres.
//
// Faithfulness matters more than realism: limits are enforced at the same
// numbers, blob presence means verified (bytes PUT without a completed
// verification are absent), selection returns each store's newest complete
// bundle, and every pre-authorized URL (bundle download, blob upload/
// download) works without the org token but not without its signature —
// exactly the contract the engine client relies on.
package cacheservice

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limits mirror the real service's enforced input bounds.
type Limits struct {
	ManifestBytes  int64
	ArchiveBytes   int64
	JSONBodyBytes  int64
	StatDigests    int
	UploadBatch    int
	MultipartParts int
}

func DefaultLimits() Limits {
	return Limits{
		ManifestBytes:  64 << 20,
		ArchiveBytes:   8 << 30,
		JSONBodyBytes:  1 << 20,
		StatDigests:    4096,
		UploadBatch:    1024,
		MultipartParts: 16,
	}
}

const (
	defaultSelectionK = 4
	maxSelectionK     = 32
	maxScopeLength    = 512
)

type Service struct {
	root   string
	token  string
	limits Limits

	// urlSecret signs the pre-authorized data-path URLs, standing in for
	// presigning: unguessable, org-token-free, per-instance.
	urlSecret []byte

	mu sync.Mutex
	// uploads maps a minted uploadID to what was promised at prepare time —
	// the completion authority, verified at uploads/complete.
	uploads map[string]stagedUpload
	// publishSeq breaks created-at ties deterministically (the real service
	// has the database's total order).
	publishSeq int64
}

type stagedUpload struct {
	Digest    string
	Size      int64
	MediaType string
}

// bundleRow is the filesystem inventory row, one JSON file per bundle.
type bundleRow struct {
	BundleID      string          `json:"bundleID"`
	Scope         string          `json:"scope"`
	StoreUUID     string          `json:"storeUUID"`
	SchemaVersion string          `json:"schemaVersion"`
	BundleFormat  int             `json:"bundleFormat"`
	EngineVersion string          `json:"engineVersion"`
	Status        string          `json:"status"`
	Counts        json.RawMessage `json:"counts,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	Seq           int64           `json:"seq"`
}

func New(root, token string) (*Service, error) {
	if root == "" {
		return nil, fmt.Errorf("test cache service: empty storage root")
	}
	if token == "" {
		return nil, fmt.Errorf("test cache service: empty token")
	}
	for _, dir := range []string{"bundles", "blobs", "staging"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return nil, fmt.Errorf("test cache service: create %s dir: %w", dir, err)
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("test cache service: generate URL secret: %w", err)
	}
	return &Service{
		root:      root,
		token:     token,
		limits:    DefaultLimits(),
		urlSecret: secret,
		uploads:   make(map[string]stagedUpload),
	}, nil
}

// Handler serves the /v1 protocol plus the /data pre-authorized paths.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	// The org-token surface.
	mux.HandleFunc("POST /v1/scopes/{scope}/bundles", s.auth(s.publishBundle))
	mux.HandleFunc("GET /v1/scopes/{scope}/bundles", s.auth(s.listBundles))
	mux.HandleFunc("POST /v1/scopes/{scope}/bundles/{bundleID}/complete", s.auth(s.completeBundle))
	mux.HandleFunc("POST /v1/blobs/stat", s.auth(s.statBlobs))
	mux.HandleFunc("POST /v1/blobs/uploads", s.auth(s.prepareBlobUpload))
	mux.HandleFunc("POST /v1/blobs/uploads/complete", s.auth(s.completeBlobUploads))
	mux.HandleFunc("GET /v1/blobs/{digest}", s.auth(s.getBlob))

	// The pre-authorized data surface (presigned-URL stand-in): signature
	// auth only, no org token.
	mux.HandleFunc("GET /data/bundles/{bundleID}", s.signed("bundle", "bundleID", s.downloadBundleData))
	mux.HandleFunc("PUT /data/uploads/{uploadID}", s.signed("upload", "uploadID", s.putUploadData))
	mux.HandleFunc("GET /data/blobs/{digest}", s.signed("blob", "digest", s.getBlobData))

	return mux
}

func (s *Service) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "missing or invalid org token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// sign derives the data-path signature for one (kind, name) pair.
func (s *Service) sign(kind, name string) string {
	mac := hmac.New(sha256.New, s.urlSecret)
	fmt.Fprintf(mac, "%s\x00%s", kind, name)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) signed(kind, pathValue string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := s.sign(kind, r.PathValue(pathValue))
		got := r.URL.Query().Get("sig")
		if got == "" || !hmac.Equal([]byte(want), []byte(got)) {
			http.Error(w, "invalid or missing URL signature", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// dataURL builds an absolute pre-authorized URL for the requesting client,
// derived from the request the way a presigner derives from its endpoint
// configuration.
func (s *Service) dataURL(r *http.Request, kind, pathSegment, name string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/data/%s/%s?sig=%s", scheme, r.Host, pathSegment, name, s.sign(kind, name))
}

func (s *Service) bundleArchivePath(bundleID string) string {
	return filepath.Join(s.root, "bundles", bundleID+".tar.zst")
}

func (s *Service) bundleRowPath(bundleID string) string {
	return filepath.Join(s.root, "bundles", bundleID+".json")
}

func (s *Service) blobPath(dgst string) string {
	return filepath.Join(s.root, "blobs", filepath.Base(dgst))
}

func (s *Service) stagingPath(uploadID string) string {
	return filepath.Join(s.root, "staging", filepath.Base(uploadID))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
