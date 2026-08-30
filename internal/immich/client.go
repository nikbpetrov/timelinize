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

// Package immich is a thin client for the parts of the Immich REST API that
// Timelinize uses to keep media in Immich: upload with dedup, album/tag
// membership, fetching originals, and updating dates/visibility. It depends
// on as few endpoints as possible (Immich's API changes between majors).
// Verified against Immich v3.1.0.
package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Visibility values for assets.
const (
	VisibilityTimeline = "timeline"
	VisibilityArchive  = "archive"
	VisibilityHidden   = "hidden"
	VisibilityLocked   = "locked"
)

// Client talks to one Immich server with one API key.
type Client struct {
	BaseURL string // e.g. http://10.0.10.32:2283 (no trailing slash)
	APIKey  string
	HTTP    *http.Client
}

// New returns a client with sane timeouts.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute}, // large videos
	}
}

// Error is an HTTP-level error from Immich.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("immich: HTTP %d: %s", e.Status, e.Body) }

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("immich: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	return c.do(ctx, method, path, body, "application/json", out)
}

// Version returns the server version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct{ Major, Minor, Patch int }
	if err := c.do(ctx, http.MethodGet, "/api/server/version", nil, "", &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch), nil
}

// Permissions returns the permissions granted to the API key.
func (c *Client) Permissions(ctx context.Context) ([]string, error) {
	var me struct {
		Permissions []string `json:"permissions"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/api-keys/me", nil, "", &me); err != nil {
		return nil, err
	}
	return me.Permissions, nil
}

// CheckResult is the outcome of BulkUploadCheck for one checksum.
type CheckResult struct {
	ID        string `json:"id"`
	Action    string `json:"action"` // accept | reject
	Reason    string `json:"reason"` // duplicate | unsupported-format
	AssetID   string `json:"assetId"`
	IsTrashed bool   `json:"isTrashed"`
}

// BulkUploadCheck asks which of the given SHA-1 checksums (hex) already exist.
// Keys of the input map are caller-chosen ids echoed back in the results.
func (c *Client) BulkUploadCheck(ctx context.Context, sha1Hex map[string]string) (map[string]CheckResult, error) {
	type item struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum"`
	}
	req := struct {
		Assets []item `json:"assets"`
	}{}
	for id, sum := range sha1Hex {
		req.Assets = append(req.Assets, item{ID: id, Checksum: sum})
	}
	var resp struct {
		Results []CheckResult `json:"results"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/assets/bulk-upload-check", req, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]CheckResult, len(resp.Results))
	for _, r := range resp.Results {
		out[r.ID] = r
	}
	return out, nil
}

// Upload describes one asset upload.
type Upload struct {
	Data       io.Reader
	Filename   string
	CreatedAt  time.Time
	ModifiedAt time.Time
	SHA1Hex    string // optional; lets the server short-circuit duplicates
	Visibility string
	// Custom metadata stored on the asset (key -> JSON object).
	Metadata map[string]any
}

// UploadResult is the server's answer to an upload.
type UploadResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // created | duplicate | replaced
}

// UploadAsset uploads an asset (multipart). A duplicate upload returns the existing id.
func (c *Client) UploadAsset(ctx context.Context, up Upload) (UploadResult, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			fields := map[string]string{
				"deviceAssetId":  up.Filename + "-" + up.CreatedAt.UTC().Format(time.RFC3339),
				"deviceId":       "timelinize",
				"fileCreatedAt":  up.CreatedAt.UTC().Format(time.RFC3339),
				"fileModifiedAt": up.ModifiedAt.UTC().Format(time.RFC3339),
				"filename":       up.Filename,
			}
			if up.Visibility != "" {
				fields["visibility"] = up.Visibility
			}
			for k, v := range fields {
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			if len(up.Metadata) > 0 {
				type kv struct {
					Key   string `json:"key"`
					Value any    `json:"value"`
				}
				var items []kv
				for k, v := range up.Metadata {
					items = append(items, kv{k, v})
				}
				b, err := json.Marshal(items)
				if err != nil {
					return err
				}
				if err := mw.WriteField("metadata", string(b)); err != nil {
					return err
				}
			}
			fw, err := mw.CreateFormFile("assetData", up.Filename)
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, up.Data); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/assets", pr)
	if err != nil {
		return UploadResult{}, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if up.SHA1Hex != "" {
		req.Header.Set("x-immich-checksum", up.SHA1Hex)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return UploadResult{}, fmt.Errorf("immich: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return UploadResult{}, &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	var out UploadResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UploadResult{}, err
	}
	return out, nil
}

// Asset is the subset of asset info we care about.
type Asset struct {
	ID               string `json:"id"`
	Checksum         string `json:"checksum"` // base64 SHA-1
	OriginalFileName string `json:"originalFileName"`
	OriginalPath     string `json:"originalPath"`
	Visibility       string `json:"visibility"`
	IsTrashed        bool   `json:"isTrashed"`
	IsOffline        bool   `json:"isOffline"`
	Type             string `json:"type"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	ExifInfo         *struct {
		FileSizeInByte   int64   `json:"fileSizeInByte"`
		ExifImageWidth   *int    `json:"exifImageWidth"`
		ExifImageHeight  *int    `json:"exifImageHeight"`
		DateTimeOriginal *string `json:"dateTimeOriginal"`
	} `json:"exifInfo"`
}

// GetAsset fetches asset info by id.
func (c *Client) GetAsset(ctx context.Context, id string) (Asset, error) {
	var a Asset
	err := c.do(ctx, http.MethodGet, "/api/assets/"+url.PathEscape(id), nil, "", &a)
	return a, err
}

// AssetUpdate holds the mutable fields we may set on an asset.
type AssetUpdate struct {
	DateTimeOriginal *time.Time
	Visibility       string
	Description      *string
	Latitude         *float64
	Longitude        *float64
}

// UpdateAsset updates dates/visibility/description of an asset (needs asset.update).
func (c *Client) UpdateAsset(ctx context.Context, id string, u AssetUpdate) error {
	body := map[string]any{}
	if u.DateTimeOriginal != nil {
		body["dateTimeOriginal"] = u.DateTimeOriginal.UTC().Format(time.RFC3339)
	}
	if u.Visibility != "" {
		body["visibility"] = u.Visibility
	}
	if u.Description != nil {
		body["description"] = *u.Description
	}
	if u.Latitude != nil && u.Longitude != nil {
		body["latitude"], body["longitude"] = *u.Latitude, *u.Longitude
	}
	if len(body) == 0 {
		return nil
	}
	return c.doJSON(ctx, http.MethodPut, "/api/assets/"+url.PathEscape(id), body, nil)
}

// Original streams the original bytes of an asset. The caller must close the reader.
func (c *Client) Original(ctx context.Context, id string) (io.ReadCloser, string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/assets/"+url.PathEscape(id)+"/original", nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("immich: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, "", 0, &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return resp.Body, resp.Header.Get("Content-Type"), resp.ContentLength, nil
}

// Album is an Immich album.
type Album struct {
	ID         string `json:"id"`
	AlbumName  string `json:"albumName"`
	AssetCount int    `json:"assetCount"`
}

// EnsureAlbum returns the id of the album with the given name, creating it if needed.
func (c *Client) EnsureAlbum(ctx context.Context, name, description string) (string, error) {
	var albums []Album
	if err := c.do(ctx, http.MethodGet, "/api/albums", nil, "", &albums); err != nil {
		return "", err
	}
	for _, a := range albums {
		if a.AlbumName == name {
			return a.ID, nil
		}
	}
	var created Album
	err := c.doJSON(ctx, http.MethodPost, "/api/albums", map[string]any{
		"albumName":   name,
		"description": description,
	}, &created)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// AddToAlbum adds assets to an album; already-present assets are not an error.
func (c *Client) AddToAlbum(ctx context.Context, albumID string, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return nil
	}
	return c.doJSON(ctx, http.MethodPut, "/api/albums/"+url.PathEscape(albumID)+"/assets", map[string]any{"ids": assetIDs}, nil)
}

// EnsureTag upserts a hierarchical tag path like "timelinize/instagram" and returns its id.
func (c *Client) EnsureTag(ctx context.Context, path string) (string, error) {
	var tags []struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodPut, "/api/tags", map[string]any{"tags": []string{path}}, &tags); err != nil {
		return "", err
	}
	for _, t := range tags {
		if t.Value == path {
			return t.ID, nil
		}
	}
	if len(tags) > 0 {
		return tags[len(tags)-1].ID, nil
	}
	return "", errors.New("immich: tag upsert returned nothing")
}

// TagAssets attaches a tag to assets (needs tag.asset).
func (c *Client) TagAssets(ctx context.Context, tagID string, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return nil
	}
	return c.doJSON(ctx, http.MethodPut, "/api/tags/"+url.PathEscape(tagID)+"/assets", map[string]any{"ids": assetIDs}, nil)
}
