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

// Package linkfetch resolves links (reels, posts, videos) found by data sources into
// downloaded media plus normalized metadata, using yt-dlp and gallery-dl with the
// user's own cookies. Results are cached on disk keyed by URL so re-imports never
// hit the network for a URL that was already resolved (or is known to be gone).
//
// The package is deliberately conservative: nothing is fetched unless enabled, one
// fetch runs at a time with a delay between fetches, and only the URL kinds listed
// in route() are ever fetched; everything else is "metadata_only".
package linkfetch

import (
	"context"
	"crypto/sha1" //nolint:gosec // cache key, not security
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Statuses of a resolution. Terminal statuses are cached and never retried.
const (
	StatusResolved     = "resolved"      // media downloaded (terminal)
	StatusMetadataOnly = "metadata_only" // kind is never fetched; only what the export told us (terminal)
	StatusExpired      = "expired"       // content that is gone by design, e.g. stories (terminal)
	StatusUnavailable  = "unavailable"   // the site says the content does not exist / is private (terminal)
	StatusFailed       = "failed"        // tool error, rate limit, login wall... retried on later imports
	StatusUnresolved   = "unresolved"    // not attempted yet (disabled, or budget for this import exhausted)
)

// Backends.
const (
	BackendYTDLP     = "ytdlp"
	BackendGalleryDL = "gallerydl"
	BackendNone      = "none"
)

// Request describes a link to resolve.
type Request struct {
	URL  string // canonical URL
	Kind string // reel, post, video, photo, story, profile, ... (see datasources/facebook/shares.go)
	Site string // data source name the cookies are keyed by: "instagram", "facebook"
}

// Result is the outcome of resolving a URL. It is what gets cached as result.json.
type Result struct {
	URL        string    `json:"url"`
	Kind       string    `json:"kind"`
	Site       string    `json:"site"`
	Backend    string    `json:"backend"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	TriedAt    time.Time `json:"tried_at"`
	DurationMS int64     `json:"duration_ms"`
	Files      []File    `json:"files,omitempty"`
	Cached     bool      `json:"-"` // true when served from the cache without touching the network
}

// File is one downloaded media file with normalized metadata.
type File struct {
	Path     string         `json:"path"` // absolute
	Name     string         `json:"name"`
	Size     int64          `json:"size"`
	Index    int            `json:"index"` // 0-based slide index
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Bytes is the total size of downloaded files.
func (r Result) Bytes() int64 {
	var n int64
	for _, f := range r.Files {
		n += f.Size
	}
	return n
}

// Resolver resolves links with caching and rate limiting. One per import.
type Resolver struct {
	opts timeline.LinkFetchOptions
	log  *zap.Logger

	mu       sync.Mutex
	lastCall time.Time
	fetches  int // network fetches performed by this resolver

	// Stats by status, for the end-of-import summary.
	stats map[string]int
}

// New creates a resolver. opts.CacheDir must be set.
func New(opts timeline.LinkFetchOptions, logger *zap.Logger) (*Resolver, error) {
	if opts.CacheDir == "" {
		return nil, errors.New("link fetch cache dir not set")
	}
	if opts.DelayMS <= 0 {
		opts.DelayMS = 3000
	}
	if opts.TimeoutSec <= 0 {
		opts.TimeoutSec = 120
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.YTDLPPath == "" {
		opts.YTDLPPath = "yt-dlp"
	}
	if opts.GalleryDLPath == "" {
		opts.GalleryDLPath = "gallery-dl"
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating link fetch cache dir: %w", err)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Resolver{opts: opts, log: logger, stats: make(map[string]int)}, nil
}

// Stats returns counts by status for everything resolved so far (including cache hits).
func (r *Resolver) Stats() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.stats))
	for k, v := range r.stats {
		out[k] = v
	}
	out["network_fetches"] = r.fetches
	return out
}

// Route decides which backend handles a request and, for kinds that are never fetched,
// the terminal status. Exported so callers can label bookmarks without a Resolver. Facebook pages/groups/events are
// never scraped; stories are gone by design; profiles and external sites are
// metadata-only.
func Route(req Request) (backend string, status string) {
	switch req.Kind {
	case "story":
		return BackendNone, StatusExpired
	case "reel", "video":
		return BackendYTDLP, ""
	case "post", "photo":
		if req.Site == "instagram" {
			return BackendGalleryDL, ""
		}
		return BackendNone, StatusMetadataOnly
	default:
		return BackendNone, StatusMetadataOnly
	}
}

func (r *Resolver) cacheDir(url string) string {
	sum := sha1.Sum([]byte(url)) //nolint:gosec // cache key
	key := hex.EncodeToString(sum[:])
	return filepath.Join(r.opts.CacheDir, key[:2], key)
}

func (r *Resolver) record(status string) {
	r.mu.Lock()
	r.stats[status]++
	r.mu.Unlock()
}

// Resolve returns the (possibly cached) result for a URL. It never returns an error
// for a failed fetch — that is a Result with StatusFailed — only for programming or
// I/O errors that make the cache unusable.
func (r *Resolver) Resolve(ctx context.Context, req Request) (Result, error) {
	dir := r.cacheDir(req.URL)
	logger := r.log.With(zap.String("url", req.URL), zap.String("kind", req.Kind))

	// cache lookup
	if cached, ok := r.loadCached(dir); ok {
		switch cached.Status {
		case StatusResolved, StatusMetadataOnly, StatusExpired, StatusUnavailable:
			cached.Cached = true
			r.record(cached.Status)
			logger.Debug("link cached", zap.String("status", cached.Status), zap.Int("files", len(cached.Files)))
			return cached, nil
		case StatusFailed:
			if cached.Attempts >= r.opts.MaxAttempts {
				cached.Cached = true
				r.record(cached.Status)
				logger.Debug("link failed permanently", zap.Int("attempts", cached.Attempts), zap.String("error", cached.Error))
				return cached, nil
			}
		}
	}

	backend, terminal := Route(req)
	res := Result{URL: req.URL, Kind: req.Kind, Site: req.Site, Backend: backend, TriedAt: time.Now()}
	if prev, ok := r.loadCached(dir); ok {
		res.Attempts = prev.Attempts
	}

	if backend == BackendNone {
		res.Status = terminal
		if err := r.store(dir, res); err != nil {
			return res, err
		}
		r.record(res.Status)
		logger.Info("link not fetchable by policy", zap.String("status", res.Status))
		return res, nil
	}

	// budget for this import
	r.mu.Lock()
	if r.opts.MaxPerImport > 0 && r.fetches >= r.opts.MaxPerImport {
		r.mu.Unlock()
		res.Status = StatusUnresolved
		r.record(res.Status)
		logger.Info("link fetch budget for this import exhausted; leaving unresolved")
		return res, nil
	}
	r.fetches++
	// rate limit: one fetch at a time, with a delay between fetches
	if wait := time.Duration(r.opts.DelayMS)*time.Millisecond - time.Since(r.lastCall); wait > 0 && !r.lastCall.IsZero() {
		r.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return res, ctx.Err()
		}
		r.mu.Lock()
	}
	r.lastCall = time.Now()
	r.mu.Unlock()

	// fresh workspace for this attempt
	if err := os.RemoveAll(dir); err != nil {
		return res, fmt.Errorf("clearing cache entry: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("creating cache entry: %w", err)
	}

	res.Attempts++
	start := time.Now()
	files, stderr, runErr := r.run(ctx, backend, req, dir)
	// gallery-dl could not help (or is missing): fall back to yt-dlp for posts
	if backend == BackendGalleryDL && len(files) == 0 {
		logger.Debug("gallery-dl produced no files; trying yt-dlp", zap.String("stderr", tail(stderr)))
		var stderr2 string
		files, stderr2, runErr = r.run(ctx, BackendYTDLP, req, dir)
		if len(files) > 0 {
			res.Backend = BackendYTDLP
		}
		stderr = strings.TrimSpace(stderr + "\n" + stderr2)
	}
	res.DurationMS = time.Since(start).Milliseconds()
	res.Files = files

	switch {
	case len(files) > 0:
		res.Status = StatusResolved
		if runErr != nil {
			// e.g. yt-dlp got the videos of a carousel but errored on image slides
			res.Error = "partial: " + tail(stderr)
		}
	case runErr != nil:
		res.Status = classifyError(stderr, runErr)
		res.Error = tail(stderr)
		if res.Error == "" {
			res.Error = runErr.Error()
		}
	default:
		res.Status = StatusFailed
		res.Error = "no files produced"
	}

	if err := r.store(dir, res); err != nil {
		return res, err
	}
	r.record(res.Status)

	fields := []zap.Field{
		zap.String("backend", res.Backend),
		zap.String("status", res.Status),
		zap.Int64("duration_ms", res.DurationMS),
		zap.Int("files", len(res.Files)),
		zap.Int64("bytes", res.Bytes()),
		zap.Int("attempt", res.Attempts),
	}
	if res.Status == StatusResolved {
		logger.Info("link fetched", fields...)
	} else {
		logger.Error("link fetch failed", append(fields, zap.String("error", res.Error))...)
	}
	return res, nil
}

func (r *Resolver) loadCached(dir string) (Result, bool) {
	var res Result
	b, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return res, false
	}
	if err := json.Unmarshal(b, &res); err != nil {
		r.log.Warn("corrupt cache entry; ignoring", zap.String("dir", dir), zap.Error(err))
		return res, false
	}
	// files must still be there for a resolved entry to count
	for _, f := range res.Files {
		if _, err := os.Stat(f.Path); err != nil {
			r.log.Warn("cached file missing; will re-fetch", zap.String("file", f.Path))
			return res, false
		}
	}
	return res, true
}

func (r *Resolver) store(dir string, res Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "result.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil { //nolint:gosec // not secret
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "result.json"))
}

// run executes a backend and returns the downloaded files (with metadata), the
// tool's stderr, and the process error if any.
func (r *Resolver) run(ctx context.Context, backend string, req Request, dir string) ([]File, string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.opts.TimeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	cookies := r.opts.Cookies[req.Site]
	switch backend {
	case BackendYTDLP:
		args := []string{
			"--no-warnings", "--no-progress", "--no-playlist-reverse",
			"--write-info-json", "--no-write-playlist-metafiles",
			"--socket-timeout", "30",
			"-o", filepath.Join(dir, "%(id)s_%(playlist_index|0)s.%(ext)s"),
		}
		if cookies != "" {
			args = append(args, "--cookies", cookies)
		}
		args = append(args, req.URL)
		cmd = exec.CommandContext(ctx, r.opts.YTDLPPath, args...) //nolint:gosec // fixed binary, URL from export
	case BackendGalleryDL:
		args := []string{"--write-metadata", "-D", dir, "--no-part"}
		if cookies != "" {
			args = append(args, "--cookies", cookies)
		}
		args = append(args, req.URL)
		cmd = exec.CommandContext(ctx, r.opts.GalleryDLPath, args...) //nolint:gosec // fixed binary, URL from export
	default:
		return nil, "", fmt.Errorf("unknown backend %q", backend)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = nil
	runErr := cmd.Run()

	files, err := collect(dir, backend)
	if err != nil {
		return nil, stderr.String(), err
	}
	return files, stderr.String(), runErr
}

// collect scans dir for downloaded media and pairs each with its metadata sidecar.
func collect(dir, backend string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		f := File{Path: filepath.Join(dir, name), Name: name, Size: info.Size()}
		switch backend {
		case BackendYTDLP:
			// <id>_<index>.<ext> -> <id>_<index>.info.json
			base := strings.TrimSuffix(name, filepath.Ext(name))
			f.Metadata, f.Index = ytdlpMetadata(filepath.Join(dir, base+".info.json"))
		case BackendGalleryDL:
			f.Metadata, f.Index = gallerydlMetadata(filepath.Join(dir, name+".json"))
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Index != files[j].Index {
			return files[i].Index < files[j].Index
		}
		return files[i].Name < files[j].Name
	})
	// normalize indexes to 0..n-1 in that order
	for i := range files {
		files[i].Index = i
	}
	return files, nil
}

// Metadata keys produced for every backend (a subset may be present).
const (
	MetaTitle       = "Title"
	MetaDescription = "Description"
	MetaAuthor      = "Author"      // username / handle
	MetaAuthorName  = "Author name" // display name
	MetaPublished   = "Published"   // time.Time
	MetaDuration    = "Duration"    // seconds (float64)
	MetaWidth       = "Width"
	MetaHeight      = "Height"
	MetaLikes       = "Likes"
	MetaComments    = "Comments"
	MetaViews       = "Views"
	MetaSourceURL   = "Source URL"
	MetaSlide       = "Slide" // "i/n"
	MetaLocation    = "Location"
	MetaFetchedWith = "Fetched with"
)

func ytdlpMetadata(path string) (map[string]any, int) {
	raw, ok := readJSON(path)
	if !ok {
		return nil, 0
	}
	m := map[string]any{MetaFetchedWith: "yt-dlp"}
	setStr(m, MetaTitle, raw["title"])
	setStr(m, MetaDescription, raw["description"])
	setStr(m, MetaAuthor, firstStr(raw["channel"], raw["uploader_id"], raw["uploader"]))
	setStr(m, MetaAuthorName, raw["uploader"])
	if ts, ok := toFloat(raw["timestamp"]); ok && ts > 0 {
		m[MetaPublished] = time.Unix(int64(ts), 0).UTC()
	}
	setNum(m, MetaDuration, raw["duration"])
	setNum(m, MetaWidth, raw["width"])
	setNum(m, MetaHeight, raw["height"])
	setNum(m, MetaLikes, raw["like_count"])
	setNum(m, MetaComments, raw["comment_count"])
	setNum(m, MetaViews, raw["view_count"])
	setStr(m, MetaSourceURL, raw["webpage_url"])
	idx := 0
	if pi, ok := toFloat(raw["playlist_index"]); ok {
		idx = int(pi)
		if n, ok := toFloat(raw["n_entries"]); ok && n > 0 {
			m[MetaSlide] = fmt.Sprintf("%d/%d", idx, int(n))
		}
	}
	return m, idx
}

func gallerydlMetadata(path string) (map[string]any, int) {
	raw, ok := readJSON(path)
	if !ok {
		return nil, 0
	}
	m := map[string]any{MetaFetchedWith: "gallery-dl"}
	setStr(m, MetaDescription, raw["description"])
	setStr(m, MetaAuthor, raw["username"])
	setStr(m, MetaAuthorName, raw["fullname"])
	if s, ok := raw["date"].(string); ok {
		if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
			m[MetaPublished] = t.UTC()
		}
	}
	setNum(m, MetaWidth, raw["width"])
	setNum(m, MetaHeight, raw["height"])
	setNum(m, MetaLikes, raw["likes"])
	setStr(m, MetaSourceURL, raw["post_url"])
	setStr(m, MetaLocation, raw["location_slug"])
	idx := 0
	if n, ok := toFloat(raw["num"]); ok {
		idx = int(n)
		if c, ok := toFloat(raw["count"]); ok && c > 0 {
			m[MetaSlide] = fmt.Sprintf("%d/%d", idx, int(c))
		}
	}
	return m, idx
}

func readJSON(path string) (map[string]any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false
	}
	return raw, true
}

func setStr(m map[string]any, key string, v any) {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		m[key] = s
	}
}

func setNum(m map[string]any, key string, v any) {
	if f, ok := toFloat(v); ok {
		m[key] = f
	}
}

func firstStr(vals ...any) any {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// classifyError maps tool output to a status: content that is gone/private is
// terminal; everything else (rate limits, login walls, network, parser errors) is
// retried on a later import.
func classifyError(stderr string, err error) string {
	s := strings.ToLower(stderr)
	for _, needle := range []string{
		"unreachable", "not available", "is not available", "no longer available",
		"404", "not found", "does not exist", "has been removed", "private account",
		"this post is private", "unavailable",
	} {
		if strings.Contains(s, needle) {
			return StatusUnavailable
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusFailed
	}
	return StatusFailed
}

// tail returns the last few lines of tool output, trimmed, for logs/metadata.
func tail(s string) string {
	s = strings.TrimSpace(s)
	const maxLen = 500
	if len(s) > maxLen {
		s = s[len(s)-maxLen:]
	}
	return s
}
