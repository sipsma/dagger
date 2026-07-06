package cacheservice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	enginecacheservice "github.com/dagger/dagger/engine/cacheservice"
	digest "github.com/opencontainers/go-digest"
)

// versionTag mirrors the real service's liberal version decode: an
// engine-owned opaque tag arriving as a JSON string or number (S7).
type versionTag string

func (v *versionTag) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*v = versionTag(asString)
		return nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*v = versionTag(asNumber.String())
		return nil
	}
	return fmt.Errorf("version tag must be a JSON string or number, got %s", data)
}

func (s *Service) publishBundle(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.limits.ManifestBytes+s.limits.ArchiveBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, fmt.Sprintf("bundle publish must be multipart: %v", err), http.StatusBadRequest)
		return
	}

	var manifestBytes []byte
	var archive *os.File
	var archiveSize int64
	defer func() {
		if archive != nil {
			archive.Close()
			os.Remove(archive.Name())
		}
	}()

	parts := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeBodyError(w, err)
			return
		}
		parts++
		if parts > s.limits.MultipartParts {
			http.Error(w, fmt.Sprintf("bundle publish exceeds %d multipart parts", s.limits.MultipartParts), http.StatusRequestEntityTooLarge)
			return
		}
		switch part.FormName() {
		case enginecacheservice.MultipartManifestField:
			if manifestBytes != nil {
				http.Error(w, "duplicate manifest part", http.StatusBadRequest)
				return
			}
			manifestBytes, err = io.ReadAll(io.LimitReader(part, s.limits.ManifestBytes+1))
			if err != nil {
				writeBodyError(w, err)
				return
			}
			if int64(len(manifestBytes)) > s.limits.ManifestBytes {
				http.Error(w, fmt.Sprintf("bundle manifest exceeds %d bytes", s.limits.ManifestBytes), http.StatusRequestEntityTooLarge)
				return
			}
		case enginecacheservice.MultipartArchiveField:
			if archive != nil {
				http.Error(w, "duplicate archive part", http.StatusBadRequest)
				return
			}
			archive, err = os.CreateTemp(filepath.Join(s.root, "staging"), "publish-*")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			archiveSize, err = io.Copy(archive, io.LimitReader(part, s.limits.ArchiveBytes+1))
			if err != nil {
				writeBodyError(w, err)
				return
			}
			if archiveSize > s.limits.ArchiveBytes {
				http.Error(w, fmt.Sprintf("bundle archive exceeds %d bytes", s.limits.ArchiveBytes), http.StatusRequestEntityTooLarge)
				return
			}
		default:
			http.Error(w, fmt.Sprintf("unexpected multipart part %q", part.FormName()), http.StatusBadRequest)
			return
		}
	}
	if manifestBytes == nil {
		http.Error(w, "bundle publish missing manifest part", http.StatusBadRequest)
		return
	}
	if archive == nil {
		http.Error(w, "bundle publish missing archive part", http.StatusBadRequest)
		return
	}

	// The manifest fields the service reads; everything else in the
	// manifest is engine business (S7).
	var manifest struct {
		BundleFormat  int             `json:"bundleFormat"`
		SchemaVersion versionTag      `json:"schemaVersion"`
		EngineVersion string          `json:"engineVersion"`
		StoreUUID     string          `json:"storeUUID"`
		Counts        json.RawMessage `json:"counts,omitempty"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		http.Error(w, fmt.Sprintf("decode bundle manifest: %v", err), http.StatusBadRequest)
		return
	}
	switch {
	case manifest.BundleFormat < 1:
		http.Error(w, fmt.Sprintf("bundle manifest bundleFormat %d is invalid", manifest.BundleFormat), http.StatusBadRequest)
		return
	case manifest.SchemaVersion == "":
		http.Error(w, "bundle manifest missing schemaVersion", http.StatusBadRequest)
		return
	case manifest.EngineVersion == "":
		http.Error(w, "bundle manifest missing engineVersion", http.StatusBadRequest)
		return
	case manifest.StoreUUID == "":
		http.Error(w, "bundle manifest missing storeUUID", http.StatusBadRequest)
		return
	}

	bundleID := newID()
	if err := archive.Sync(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Archive lands before the inventory row (S6): a listed bundle always
	// has its archive.
	if err := os.Rename(archive.Name(), s.bundleArchivePath(bundleID)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	archive.Close()
	archive = nil

	s.mu.Lock()
	s.publishSeq++
	row := bundleRow{
		BundleID:      bundleID,
		Scope:         scope,
		StoreUUID:     manifest.StoreUUID,
		SchemaVersion: string(manifest.SchemaVersion),
		BundleFormat:  manifest.BundleFormat,
		EngineVersion: manifest.EngineVersion,
		Status:        enginecacheservice.BundleStatusPending,
		Counts:        manifest.Counts,
		CreatedAt:     time.Now().UTC(),
		Seq:           s.publishSeq,
	}
	err = s.writeBundleRowLocked(row)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, enginecacheservice.PublishBundleResponse{BundleID: bundleID})
}

func (s *Service) completeBundle(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	bundleID := r.PathValue("bundleID")
	if !validID(bundleID) {
		http.Error(w, fmt.Sprintf("invalid bundle ID %q", bundleID), http.StatusBadRequest)
		return
	}

	// The optional tally decodes through the whitelist type; it is accepted
	// and discarded (the real service records it in its event log).
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.limits.JSONBodyBytes))
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		var tally enginecacheservice.BundleUploadTally
		if err := json.Unmarshal(body, &tally); err != nil {
			http.Error(w, fmt.Sprintf("decode upload tally: %v", err), http.StatusBadRequest)
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.readBundleRowLocked(bundleID)
	if err != nil || row.Scope != scope {
		http.Error(w, "unknown bundle", http.StatusNotFound)
		return
	}
	row.Status = enginecacheservice.BundleStatusComplete
	if err := s.writeBundleRowLocked(*row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct{}{})
}

func (s *Service) listBundles(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	schemaVersion := query.Get("schemaVersion")
	if schemaVersion == "" {
		http.Error(w, "schemaVersion query parameter is required", http.StatusBadRequest)
		return
	}
	bundleFormat, err := strconv.Atoi(query.Get("bundleFormat"))
	if err != nil || bundleFormat < 1 {
		http.Error(w, "bundleFormat query parameter must be a positive integer", http.StatusBadRequest)
		return
	}
	limit := defaultSelectionK
	if rawLimit := query.Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 {
			http.Error(w, "limit query parameter must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(limit, maxSelectionK)
	}

	s.mu.Lock()
	rows, err := s.readAllBundleRowsLocked()
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Selection semantics (§10 D3, as the real service built them): exact
	// version match within the scope; one bundle per exporter store — its
	// newest COMPLETE bundle even when a newer pending exists, else its
	// newest pending; stores ordered newest first; up to K.
	perStore := make(map[string]bundleRow)
	for _, row := range rows {
		if row.Scope != scope || row.SchemaVersion != schemaVersion || row.BundleFormat != bundleFormat {
			continue
		}
		best, exists := perStore[row.StoreUUID]
		if !exists || bundleWins(row, best) {
			perStore[row.StoreUUID] = row
		}
	}
	selected := make([]bundleRow, 0, len(perStore))
	for _, row := range perStore {
		selected = append(selected, row)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Seq > selected[j].Seq })
	if len(selected) > limit {
		selected = selected[:limit]
	}

	resp := enginecacheservice.ListBundlesResponse{Bundles: make([]enginecacheservice.BundleSummary, 0, len(selected))}
	for _, row := range selected {
		resp.Bundles = append(resp.Bundles, enginecacheservice.BundleSummary{
			BundleID:    row.BundleID,
			StoreUUID:   row.StoreUUID,
			CreatedAt:   row.CreatedAt,
			Status:      row.Status,
			DownloadURL: s.dataURL(r, "bundle", "bundles", row.BundleID),
			Counts:      row.Counts,
		})
	}
	writeJSON(w, resp)
}

// bundleWins reports whether a beats b as one store's selected bundle:
// complete beats pending regardless of age; within a status, newest wins.
func bundleWins(a, b bundleRow) bool {
	aComplete := a.Status == enginecacheservice.BundleStatusComplete
	bComplete := b.Status == enginecacheservice.BundleStatusComplete
	if aComplete != bComplete {
		return aComplete
	}
	return a.Seq > b.Seq
}

func (s *Service) statBlobs(w http.ResponseWriter, r *http.Request) {
	var req enginecacheservice.BlobStatRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyError(w, err)
		return
	}
	if len(req.Digests) > s.limits.StatDigests {
		http.Error(w, fmt.Sprintf("stat batch exceeds %d digests", s.limits.StatDigests), http.StatusRequestEntityTooLarge)
		return
	}
	missing := []string{}
	for _, raw := range req.Digests {
		if _, err := digest.Parse(raw); err != nil {
			http.Error(w, fmt.Sprintf("invalid digest %q: %v", raw, err), http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(s.blobPath(raw)); err != nil {
			missing = append(missing, raw)
		}
	}
	writeJSON(w, enginecacheservice.BlobStatResponse{Missing: missing})
}

func (s *Service) prepareBlobUpload(w http.ResponseWriter, r *http.Request) {
	var req enginecacheservice.BlobUploadRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyError(w, err)
		return
	}
	dgst, err := digest.Parse(req.Digest)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid digest %q: %v", req.Digest, err), http.StatusBadRequest)
		return
	}
	if req.Size <= 0 {
		http.Error(w, fmt.Sprintf("invalid blob size %d", req.Size), http.StatusBadRequest)
		return
	}

	if info, err := os.Stat(s.blobPath(dgst.String())); err == nil {
		if info.Size() != req.Size {
			http.Error(w, fmt.Sprintf("blob %s already verified with size %d, not %d", req.Digest, info.Size(), req.Size), http.StatusConflict)
			return
		}
		writeJSON(w, enginecacheservice.BlobUploadResponse{AlreadyExists: true})
		return
	}

	uploadID := newID()
	s.mu.Lock()
	s.uploads[uploadID] = stagedUpload{Digest: dgst.String(), Size: req.Size, MediaType: req.MediaType}
	s.mu.Unlock()
	writeJSON(w, enginecacheservice.BlobUploadResponse{
		Method:   http.MethodPut,
		URL:      s.dataURL(r, "upload", "uploads", uploadID),
		UploadID: uploadID,
	})
}

func (s *Service) completeBlobUploads(w http.ResponseWriter, r *http.Request) {
	var req enginecacheservice.CompleteBlobUploadsRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyError(w, err)
		return
	}
	if len(req.Blobs) > s.limits.UploadBatch {
		http.Error(w, fmt.Sprintf("completion batch exceeds %d blobs", s.limits.UploadBatch), http.StatusRequestEntityTooLarge)
		return
	}

	var resp enginecacheservice.CompleteBlobUploadsResponse
	for _, item := range req.Blobs {
		dgst, err := digest.Parse(item.Digest)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid digest %q: %v", item.Digest, err), http.StatusBadRequest)
			return
		}
		outcome := s.completeSingleUpload(dgst, item)
		switch {
		case outcome.failure != "":
			resp.Failed = append(resp.Failed, enginecacheservice.BlobUploadFailure{Digest: item.Digest, Error: outcome.failure})
		case outcome.alreadyExists:
			resp.AlreadyExists = append(resp.AlreadyExists, item.Digest)
		default:
			resp.Verified = append(resp.Verified, item.Digest)
		}
	}
	writeJSON(w, resp)
}

type completionOutcome struct {
	alreadyExists bool
	failure       string
}

// completeSingleUpload verifies one staged upload against its prepare-time
// promise and the actual bytes, then promotes it into the verified blob
// set — presence means verified, the same contract as the real service.
func (s *Service) completeSingleUpload(dgst digest.Digest, item enginecacheservice.BlobUploadCompletion) completionOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()

	if info, err := os.Stat(s.blobPath(dgst.String())); err == nil {
		if info.Size() != item.Size {
			return completionOutcome{failure: "blob already recorded with a different size"}
		}
		return completionOutcome{alreadyExists: true}
	}

	staged, ok := s.uploads[item.UploadID]
	if !ok {
		return completionOutcome{failure: "invalid or expired upload token"}
	}
	if staged.Digest != dgst.String() || staged.Size != item.Size {
		return completionOutcome{failure: "upload token does not match this blob"}
	}

	stagingPath := s.stagingPath(item.UploadID)
	f, err := os.Open(stagingPath)
	if err != nil {
		return completionOutcome{failure: "no uploaded bytes for this upload token"}
	}
	digester := dgst.Algorithm().Digester()
	n, err := io.Copy(digester.Hash(), f)
	f.Close()
	if err != nil {
		return completionOutcome{failure: fmt.Sprintf("read staged upload: %v", err)}
	}
	if n != item.Size {
		return completionOutcome{failure: fmt.Sprintf("uploaded %d bytes, expected %d", n, item.Size)}
	}
	if computed := digester.Digest(); computed != dgst {
		return completionOutcome{failure: fmt.Sprintf("uploaded bytes digest %s does not match %s", computed, dgst)}
	}

	if err := os.Rename(stagingPath, s.blobPath(dgst.String())); err != nil {
		return completionOutcome{failure: fmt.Sprintf("promote blob: %v", err)}
	}
	delete(s.uploads, item.UploadID)
	return completionOutcome{}
}

func (s *Service) getBlob(w http.ResponseWriter, r *http.Request) {
	dgst, err := digest.Parse(r.PathValue("digest"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid digest %q: %v", r.PathValue("digest"), err), http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(s.blobPath(dgst.String())); err != nil {
		http.Error(w, "unknown blob", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, s.dataURL(r, "blob", "blobs", dgst.String()), http.StatusTemporaryRedirect)
}

//
// The pre-authorized data surface.
//

func (s *Service) downloadBundleData(w http.ResponseWriter, r *http.Request) {
	bundleID := r.PathValue("bundleID")
	if !validID(bundleID) {
		http.Error(w, "invalid bundle ID", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, s.bundleArchivePath(bundleID))
}

func (s *Service) putUploadData(w http.ResponseWriter, r *http.Request) {
	uploadID := r.PathValue("uploadID")
	s.mu.Lock()
	staged, ok := s.uploads[uploadID]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	f, err := os.OpenFile(s.stagingPath(uploadID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// One byte over the promised size fails the PUT early; the exact-size
	// check happens at completion verification.
	_, err = io.Copy(f, io.LimitReader(r.Body, staged.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Service) getBlobData(w http.ResponseWriter, r *http.Request) {
	dgst, err := digest.Parse(r.PathValue("digest"))
	if err != nil {
		http.Error(w, "invalid digest", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, s.blobPath(dgst.String()))
}

//
// Inventory + helpers.
//

func (s *Service) writeBundleRowLocked(row bundleRow) error {
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return os.WriteFile(s.bundleRowPath(row.BundleID), encoded, 0o644)
}

func (s *Service) readBundleRowLocked(bundleID string) (*bundleRow, error) {
	raw, err := os.ReadFile(s.bundleRowPath(bundleID))
	if err != nil {
		return nil, err
	}
	var row bundleRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Service) readAllBundleRowsLocked() ([]bundleRow, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "bundles"))
	if err != nil {
		return nil, err
	}
	var rows []bundleRow
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		row, err := s.readBundleRowLocked(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
	}
	return rows, nil
}

func (s *Service) decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.limits.JSONBodyBytes))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

func scopeFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	scope := r.PathValue("scope")
	if scope == "" || len(scope) > maxScopeLength || strings.ContainsAny(scope, "\x00\n\r") {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return "", false
	}
	return scope, true
}

func writeBodyError(w http.ResponseWriter, err error) {
	if maxBytesErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
		http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit), http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// newID mints a UUID-shaped random identifier (the real service mints
// UUIDs; ID shape is not part of the protocol but staying in-shape keeps
// clients honest about treating IDs as opaque).
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := hex.EncodeToString(b[:])
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

// validID accepts the IDs this service mints: hex-and-dash, non-empty, and
// path-safe (they name files under the storage root).
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c == '-':
		default:
			return false
		}
	}
	return true
}
