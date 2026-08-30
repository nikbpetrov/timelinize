/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Immich's checksum algorithm
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/timelinize/timelinize/internal/immich"
	"go.uber.org/zap"
)

// ImmichOptions configures Immich as the canonical store for image/video data files.
// See docs/fork/immich-media-store.md.
type ImmichOptions struct {
	Enabled bool `json:"enabled,omitempty"`

	// Base URL of the Immich server, e.g. "http://10.0.10.32:2283".
	URL string `json:"url,omitempty"`

	// API key, or a file containing it (file preferred; keeps the key out of config.json).
	APIKey     string `json:"api_key,omitempty"`
	APIKeyFile string `json:"api_key_file,omitempty"`

	// Album all uploads are added to (default "Timelinize").
	Album string `json:"album,omitempty"`

	// Tag prefix; assets get "<prefix>/<data source>" (default "timelinize"; empty string disables... use "-" to disable).
	TagPrefix string `json:"tag_prefix,omitempty"`

	// Visibility of uploaded assets (default "archive": out of the main timeline, visible in Archive and the album).
	Visibility string `json:"visibility,omitempty"`

	// Whether to queue an upload job after every import (default true).
	UploadAfterImport *bool `json:"upload_after_import,omitempty"`

	// Whether the upload job deletes the local copy once the asset is confirmed in Immich
	// (default false). Local copies are restored on demand when needed.
	EvictAfterUpload bool `json:"evict_after_upload,omitempty"`
}

func (o ImmichOptions) uploadAfterImport() bool {
	return o.UploadAfterImport == nil || *o.UploadAfterImport
}

// immichStore is the per-timeline Immich connection.
type immichStore struct {
	opts   ImmichOptions
	client *immich.Client

	mu      sync.Mutex
	albumID string
	tagIDs  map[string]string
}

// SetImmich configures (or, with Enabled=false, clears) the Immich media store for this timeline.
func (tl *Timeline) SetImmich(opts ImmichOptions) error {
	if !opts.Enabled {
		tl.immich = nil
		return nil
	}
	if opts.URL == "" {
		return errors.New("immich: url is required")
	}
	key := opts.APIKey
	if key == "" && opts.APIKeyFile != "" {
		b, err := os.ReadFile(opts.APIKeyFile)
		if err != nil {
			return fmt.Errorf("immich: reading api key file: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		return errors.New("immich: api_key or api_key_file is required")
	}
	if opts.Album == "" {
		opts.Album = "Timelinize"
	}
	if opts.TagPrefix == "" {
		opts.TagPrefix = "timelinize"
	}
	if opts.Visibility == "" {
		opts.Visibility = immich.VisibilityArchive
	}
	tl.immich = &immichStore{
		opts:   opts,
		client: immich.New(opts.URL, key),
		tagIDs: make(map[string]string),
	}
	Log.Named("immich").Info("immich media store configured",
		zap.String("repo", tl.id.String()),
		zap.String("url", opts.URL),
		zap.String("album", opts.Album),
		zap.String("visibility", opts.Visibility),
		zap.Bool("upload_after_import", opts.uploadAfterImport()),
		zap.Bool("evict_after_upload", opts.EvictAfterUpload))
	return nil
}

// ImmichEnabled reports whether this timeline has an Immich store configured.
func (tl *Timeline) ImmichEnabled() bool { return tl.immich != nil }

func (s *immichStore) album(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.albumID != "" {
		return s.albumID, nil
	}
	id, err := s.client.EnsureAlbum(ctx, s.opts.Album, "Media imported by Timelinize (archived; not in the main timeline).")
	if err != nil {
		return "", err
	}
	s.albumID = id
	return id, nil
}

func (s *immichStore) tag(ctx context.Context, dataSource string) (string, error) {
	if s.opts.TagPrefix == "-" {
		return "", nil
	}
	tagPath := s.opts.TagPrefix + "/" + dataSource
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.tagIDs[tagPath]; ok {
		return id, nil
	}
	id, err := s.client.EnsureTag(ctx, tagPath)
	if err != nil {
		return "", err
	}
	s.tagIDs[tagPath] = id
	return id, nil
}

// ImmichStatus summarizes the Immich mapping for this repo.
type ImmichStatus struct {
	Enabled       bool   `json:"enabled"`
	URL           string `json:"url,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
	ServerError   string `json:"server_error,omitempty"`
	Album         string `json:"album,omitempty"`

	// counts
	EligibleFiles int64 `json:"eligible_files"` // distinct image/video data files in the repo
	Uploaded      int64 `json:"uploaded"`       // rows in immich_assets
	Evicted       int64 `json:"evicted"`        // of which the local copy was deleted
	Bytes         int64 `json:"bytes"`          // total size of uploaded files
}

// ImmichStatus returns counts and connectivity information.
func (tl *Timeline) ImmichStatus(ctx context.Context) (ImmichStatus, error) {
	var st ImmichStatus
	if tl.immich != nil {
		st.Enabled = true
		st.URL = tl.immich.opts.URL
		st.Album = tl.immich.opts.Album
		if v, err := tl.immich.client.Version(ctx); err != nil {
			st.ServerError = err.Error()
		} else {
			st.ServerVersion = v
		}
	}
	err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT count(DISTINCT data_hash) FROM items
		WHERE data_file IS NOT NULL AND data_hash IS NOT NULL AND (data_type LIKE 'image/%' OR data_type LIKE 'video/%')`).Scan(&st.EligibleFiles)
	if err != nil {
		return st, err
	}
	err = tl.db.ReadPool.QueryRowContext(ctx, `SELECT count(), coalesce(sum(evicted IS NOT NULL), 0), coalesce(sum(size), 0) FROM immich_assets`).
		Scan(&st.Uploaded, &st.Evicted, &st.Bytes)
	return st, err
}

// CreateImmichJob queues an upload job. importJobID limits it to items from that import
// (0 = the whole repository). evict deletes local copies after confirming the upload.
func (tl *Timeline) CreateImmichJob(importJobID uint64, evict bool) (uint64, error) {
	if tl.immich == nil {
		return 0, errors.New("immich is not configured for this repository")
	}
	return tl.CreateJob(immichJob{ItemsFromImportJob: importJobID, Evict: evict}, time.Time{}, 0, 0, importJobID)
}

// uploadToImmichForImportedItems queues the upload job after an import, if configured.
func (ij ImportJob) uploadToImmichForImportedItems() {
	store := ij.job.tl.immich
	if store == nil || !store.opts.uploadAfterImport() {
		return
	}
	ij.job.Logger().Info("creating Immich upload job from import")
	if _, err := ij.job.tl.CreateImmichJob(ij.job.ID(), store.opts.EvictAfterUpload); err != nil {
		ij.job.Logger().Error("creating Immich upload job", zap.Error(err))
	}
}

// immichJob uploads image/video data files to Immich and records the mapping.
type immichJob struct {
	// Only items from this import job (0 = all items in the repo).
	ItemsFromImportJob uint64 `json:"items_from_import_job,omitempty"`

	// Delete the local copy after the asset is confirmed in Immich.
	Evict bool `json:"evict,omitempty"`
}

type immichCheckpoint struct {
	LastItemID int64 `json:"last_item_id"`
}

type immichCandidate struct {
	itemID     int64
	dataFile   string
	dataHash   []byte
	dataType   string
	timestamp  *int64
	stored     int64
	dataSource string
	assetID    string // non-empty if already uploaded
	evicted    bool
}

const immichJobPageSize = 200

// candidatesQuery pages through distinct eligible data files. Items sharing a data file are
// collapsed by the "min(id)" grouping so each file is handled once per job.
const immichCandidatesQuery = `
	SELECT min(items.id), items.data_file, items.data_hash, items.data_type, items.timestamp, items.stored, data_sources.name,
		immich_assets.asset_id, immich_assets.evicted IS NOT NULL
	FROM items
	JOIN data_sources ON data_sources.id = items.data_source_id
	LEFT JOIN immich_assets ON immich_assets.data_hash = items.data_hash
	WHERE items.data_file IS NOT NULL AND items.data_hash IS NOT NULL AND items.deleted IS NULL
		AND (items.data_type LIKE 'image/%' OR items.data_type LIKE 'video/%')
		AND (? = 0 OR items.job_id = ? OR items.modified_job_id = ?)
	GROUP BY items.data_hash
	HAVING min(items.id) > ?
	ORDER BY min(items.id)
	LIMIT ?`

func (j immichJob) Run(job *ActiveJob, checkpoint []byte) error {
	tl := job.tl
	store := tl.immich
	if store == nil {
		return errors.New("immich is not configured for this repository")
	}
	logger := job.Logger().Named("immich").With(zap.Uint64("import_job_id", j.ItemsFromImportJob), zap.Bool("evict", j.Evict))

	var chk immichCheckpoint
	if checkpoint != nil {
		if err := json.Unmarshal(checkpoint, &chk); err != nil {
			logger.Error("failed to resume from checkpoint", zap.Error(err))
		} else {
			logger.Info("resuming from checkpoint", zap.Int64("last_item_id", chk.LastItemID))
		}
	}

	if v, err := store.client.Version(job.ctx); err != nil {
		return fmt.Errorf("immich unreachable (%s): %w", store.opts.URL, err)
	} else {
		logger.Info("connected to Immich", zap.String("version", v), zap.String("url", store.opts.URL))
	}
	albumID, err := store.album(job.ctx)
	if err != nil {
		return fmt.Errorf("immich album: %w", err)
	}

	// total = distinct eligible files (for progress)
	var total int
	err = tl.db.ReadPool.QueryRowContext(job.ctx, `SELECT count(DISTINCT data_hash) FROM items
		WHERE data_file IS NOT NULL AND data_hash IS NOT NULL AND deleted IS NULL
		AND (data_type LIKE 'image/%' OR data_type LIKE 'video/%')
		AND (? = 0 OR job_id = ? OR modified_job_id = ?)`, j.ItemsFromImportJob, j.ItemsFromImportJob, j.ItemsFromImportJob).Scan(&total)
	if err != nil {
		return fmt.Errorf("counting eligible files: %w", err)
	}
	job.SetTotal(total)
	logger.Info("uploading media to Immich", zap.Int("files", total))

	stats := map[string]int{}
	var bytesUploaded int64
	lastID := chk.LastItemID
	for {
		if err := job.Continue(); err != nil {
			return err
		}
		page, err := j.loadPage(job.ctx, tl, lastID)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			lastID = c.itemID
			outcome, n, err := j.processOne(job.ctx, tl, store, albumID, logger, c)
			if err != nil {
				logger.Error("immich sync failed for file",
					zap.Int64("item_id", c.itemID),
					zap.String("data_file", c.dataFile),
					zap.String("outcome", outcome),
					zap.Error(err))
				outcome = "error"
			}
			stats[outcome]++
			bytesUploaded += n
			job.Progress(1)
		}
		if err := job.Checkpoint(immichCheckpoint{LastItemID: lastID}); err != nil {
			logger.Error("saving checkpoint", zap.Error(err))
		}
	}

	logger.Info("immich sync summary", zap.Any("outcomes", stats), zap.Int64("bytes_uploaded", bytesUploaded))
	if stats["error"] > 0 {
		job.Message(fmt.Sprintf("Done with %d errors (see log)", stats["error"]))
	}
	return nil
}

func (j immichJob) loadPage(ctx context.Context, tl *Timeline, afterID int64) ([]immichCandidate, error) {
	rows, err := tl.db.ReadPool.QueryContext(ctx, immichCandidatesQuery,
		j.ItemsFromImportJob, j.ItemsFromImportJob, j.ItemsFromImportJob, afterID, immichJobPageSize)
	if err != nil {
		return nil, fmt.Errorf("querying candidates: %w", err)
	}
	defer rows.Close()
	var page []immichCandidate
	for rows.Next() {
		var c immichCandidate
		var assetID *string
		var dataType *string
		if err := rows.Scan(&c.itemID, &c.dataFile, &c.dataHash, &dataType, &c.timestamp, &c.stored, &c.dataSource, &assetID, &c.evicted); err != nil {
			return nil, fmt.Errorf("scanning candidate: %w", err)
		}
		if assetID != nil {
			c.assetID = *assetID
		}
		if dataType != nil {
			c.dataType = *dataType
		}
		page = append(page, c)
	}
	return page, rows.Err()
}

// processOne uploads (or verifies) one file and optionally evicts the local copy.
// It returns an outcome label for the summary and the number of bytes uploaded.
func (j immichJob) processOne(ctx context.Context, tl *Timeline, store *immichStore, albumID string, logger *zap.Logger, c immichCandidate) (string, int64, error) {
	fullPath := tl.FullPath(c.dataFile)
	log := logger.With(zap.Int64("item_id", c.itemID), zap.String("data_file", c.dataFile))

	if c.assetID != "" {
		// already mapped; nothing to upload
		if j.Evict && !c.evicted {
			return j.evict(ctx, tl, store, log, c, fullPath)
		}
		log.Debug("already in Immich", zap.String("asset_id", c.assetID))
		return "already_uploaded", 0, nil
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return "missing_local", 0, fmt.Errorf("local data file missing: %w", err)
	}

	sha, err := fileSHA1(fullPath)
	if err != nil {
		return "hash_error", 0, err
	}

	start := time.Now()
	status := "created"
	assetID := ""
	check, err := store.client.BulkUploadCheck(ctx, map[string]string{"f": sha})
	if err != nil {
		return "check_failed", 0, fmt.Errorf("bulk upload check: %w", err)
	}
	if r, ok := check["f"]; ok && r.Action == "reject" {
		switch r.Reason {
		case "duplicate":
			if r.IsTrashed {
				log.Warn("asset exists in Immich but is in the trash; it will disappear after the trash period",
					zap.String("asset_id", r.AssetID))
			}
			status, assetID = "duplicate", r.AssetID
		default:
			return "unsupported", 0, fmt.Errorf("immich rejected file: %s", r.Reason)
		}
	}

	var uploaded int64
	if assetID == "" {
		f, err := os.Open(fullPath)
		if err != nil {
			return "open_error", 0, err
		}
		created := c.stored
		if c.timestamp != nil {
			created = *c.timestamp
		}
		res, err := store.client.UploadAsset(ctx, immich.Upload{
			Data:       f,
			Filename:   "tlz-" + c.dataSource + "-" + path.Base(c.dataFile),
			CreatedAt:  time.UnixMilli(created),
			ModifiedAt: time.UnixMilli(created),
			SHA1Hex:    sha,
			Visibility: store.opts.Visibility,
			Metadata: map[string]any{
				"timelinize": map[string]any{
					"repo":        tl.id.String(),
					"item_id":     c.itemID,
					"data_source": c.dataSource,
					"data_file":   c.dataFile,
					"data_hash":   hex.EncodeToString(c.dataHash),
				},
			},
		})
		f.Close()
		if err != nil {
			return "upload_failed", 0, fmt.Errorf("uploading: %w", err)
		}
		status, assetID = res.Status, res.ID
		if status == "created" {
			uploaded = info.Size()
		}
	}

	// album + tag membership (best effort; the mapping is what matters)
	if err := store.client.AddToAlbum(ctx, albumID, []string{assetID}); err != nil {
		log.Warn("adding asset to album", zap.String("asset_id", assetID), zap.Error(err))
	}
	if tagID, err := store.tag(ctx, c.dataSource); err != nil {
		log.Warn("ensuring tag", zap.Error(err))
	} else if tagID != "" {
		if err := store.client.TagAssets(ctx, tagID, []string{assetID}); err != nil {
			log.Warn("tagging asset", zap.String("asset_id", assetID), zap.Error(err))
		}
	}

	_, err = tl.db.WritePool.ExecContext(ctx, `INSERT OR REPLACE INTO immich_assets
		(data_hash, asset_id, sha1, data_file, media_type, size, item_id, uploaded, status, evicted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		c.dataHash, assetID, sha, c.dataFile, c.dataType, info.Size(), c.itemID, time.Now().UnixMilli(), status)
	if err != nil {
		return "db_error", uploaded, fmt.Errorf("recording asset mapping: %w", err)
	}

	log.Info("asset in Immich",
		zap.String("asset_id", assetID),
		zap.String("status", status),
		zap.Int64("bytes", info.Size()),
		zap.Duration("duration", time.Since(start)))

	if j.Evict {
		c.assetID = assetID
		outcome, _, err := j.evict(ctx, tl, store, log, c, fullPath)
		return outcome, uploaded, err
	}
	return status, uploaded, nil
}

// evict deletes the local copy of a file that is confirmed present (and not trashed) in Immich.
func (j immichJob) evict(ctx context.Context, tl *Timeline, store *immichStore, log *zap.Logger, c immichCandidate, fullPath string) (string, int64, error) {
	asset, err := store.client.GetAsset(ctx, c.assetID)
	if err != nil {
		return "evict_check_failed", 0, fmt.Errorf("verifying asset before evicting local copy: %w", err)
	}
	if asset.IsTrashed || asset.IsOffline {
		return "evict_refused", 0, fmt.Errorf("asset %s is trashed/offline in Immich; keeping local copy", c.assetID)
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "evict_failed", 0, err
	}
	_, err = tl.db.WritePool.ExecContext(ctx, `UPDATE immich_assets SET evicted=? WHERE data_hash=?`, time.Now().UnixMilli(), c.dataHash)
	if err != nil {
		return "db_error", 0, err
	}
	log.Info("evicted local copy; Immich is now the only copy", zap.String("asset_id", c.assetID))
	return "evicted", 0, nil
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New() //nolint:gosec // Immich's checksum algorithm
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureDataFile makes sure the data file (repo-relative) exists locally, restoring it
// from Immich if it was evicted. It is a no-op when the file is present or Immich is not
// configured. Call it before opening a data file for reading.
func (tl *Timeline) EnsureDataFile(ctx context.Context, dataFile string) error {
	if dataFile == "" {
		return nil
	}
	fullPath := tl.FullPath(dataFile)
	if _, err := os.Stat(fullPath); err == nil {
		return nil
	}
	if tl.immich == nil {
		return nil // nothing we can do; caller will get the usual not-found error
	}

	var assetID string
	var dataHash []byte
	err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT ia.asset_id, ia.data_hash FROM items
		JOIN immich_assets ia ON ia.data_hash = items.data_hash
		WHERE items.data_file = ? LIMIT 1`, dataFile).Scan(&assetID, &dataHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not in Immich either
	}
	if err != nil {
		return fmt.Errorf("looking up Immich asset for %s: %w", dataFile, err)
	}

	logger := Log.Named("immich").With(zap.String("data_file", dataFile), zap.String("asset_id", assetID))
	start := time.Now()

	body, _, _, err := tl.immich.client.Original(ctx, assetID)
	if err != nil {
		logger.Error("restoring data file from Immich failed", zap.Error(err))
		return fmt.Errorf("restoring %s from Immich: %w", dataFile, err)
	}
	defer body.Close()

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(fullPath), ".restore-*")
	if err != nil {
		return err
	}
	h := newHash()
	n, err := io.Copy(io.MultiWriter(tmp, h), body)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("downloading original from Immich: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), dataHash) {
		os.Remove(tmp.Name())
		logger.Error("restored file does not match the recorded checksum; discarding",
			zap.String("expected", hex.EncodeToString(dataHash)), zap.String("actual", hex.EncodeToString(h.Sum(nil))))
		return fmt.Errorf("restored %s from Immich but checksum differs", dataFile)
	}
	if err := os.Rename(tmp.Name(), fullPath); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	_, _ = tl.db.WritePool.ExecContext(ctx, `UPDATE immich_assets SET evicted=NULL WHERE data_hash=?`, dataHash)
	logger.Info("restored data file from Immich", zap.Int64("bytes", n), zap.Duration("duration", time.Since(start)))
	return nil
}
