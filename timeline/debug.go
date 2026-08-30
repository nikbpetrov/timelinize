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
	"context"
	"crypto/sha1" //nolint:gosec // cache key, matches internal/linkfetch
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ItemDebug is everything the repository knows about one item, for troubleshooting
// (fork). It is deliberately raw: table rows as stored, every relationship in both
// directions, the owner's attribute/entity chain, the data file on disk, the Immich
// mapping, the link-fetch cache entry, and the job that created the item.
type ItemDebug struct {
	Item            map[string]any           `json:"item"`            // items row, all columns (hashes hex-encoded)
	Classification  string                   `json:"classification"`  // name
	DataSource      string                   `json:"data_source"`     // name
	Owner           *DebugEntity             `json:"owner,omitempty"` // via items.attribute_id
	EdgesOut        []DebugEdge              `json:"edges_out"`
	EdgesIn         []DebugEdge              `json:"edges_in"`
	DataFile        *DebugFile               `json:"data_file,omitempty"`
	Immich          *DebugImmich             `json:"immich,omitempty"`
	LinkFetch       map[string]any           `json:"link_fetch,omitempty"` // result.json of the cache entry for a bookmark's URL
	Job             map[string]any           `json:"job,omitempty"`
	Thumbnail       map[string]any           `json:"thumbnail,omitempty"`
	Warnings        []string                 `json:"warnings,omitempty"`
	Generated       time.Time                `json:"generated"`
	RepoDir         string                   `json:"repo_dir"`
	Entities        []DebugEntity            `json:"entities,omitempty"`         // every entity touched by an edge, with all attributes
	ItemsReferenced map[int64]map[string]any `json:"items_referenced,omitempty"` // summaries of items at the other end of edges
}

// DebugEntity is an entity with its attributes as stored.
type DebugEntity struct {
	ID           int64            `json:"id"`
	Type         string           `json:"type"`
	Name         string           `json:"name"`
	Attributes   []map[string]any `json:"attributes"`
	ViaAttribute map[string]any   `json:"via_attribute,omitempty"` // the attribute row the edge/owner points at
}

// DebugEdge is a relationships row with names resolved.
type DebugEdge struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Directed    bool   `json:"directed"`
	Value       any    `json:"value,omitempty"`
	ItemID      *int64 `json:"item_id,omitempty"`      // other item
	AttributeID *int64 `json:"attribute_id,omitempty"` // other attribute
	EntityID    *int64 `json:"entity_id,omitempty"`    // entity owning that attribute
	Entity      string `json:"entity,omitempty"`
	Attribute   string `json:"attribute,omitempty"` // name=value
	Start       *int64 `json:"start,omitempty"`
	End         *int64 `json:"end,omitempty"`
	Metadata    any    `json:"metadata,omitempty"`
}

// DebugFile describes the data file on disk.
type DebugFile struct {
	RepoRelative string `json:"repo_relative"`
	FullPath     string `json:"full_path"`
	Exists       bool   `json:"exists"`
	Size         int64  `json:"size,omitempty"`
	ModTime      string `json:"mod_time,omitempty"`
	BLAKE3       string `json:"blake3,omitempty"`
	HashMatches  *bool  `json:"hash_matches_db,omitempty"`
	ServeURL     string `json:"serve_url"`
}

// DebugImmich is the immich_assets row plus handy URLs.
type DebugImmich struct {
	AssetID  string `json:"asset_id"`
	SHA1     string `json:"sha1"`
	Status   string `json:"status"`
	Uploaded string `json:"uploaded"`
	Evicted  string `json:"evicted,omitempty"`
	Size     int64  `json:"size"`
	DataFile string `json:"data_file"`
	WebURL   string `json:"web_url,omitempty"`
	APIURL   string `json:"api_url,omitempty"`
	Original string `json:"original_url,omitempty"`
}

// DebugItem gathers ItemDebug for an item.
func (tl *Timeline) DebugItem(ctx context.Context, itemID int64) (*ItemDebug, error) {
	d := &ItemDebug{Generated: time.Now(), RepoDir: tl.repoDir, ItemsReferenced: map[int64]map[string]any{}}
	db := tl.db.ReadPool

	row, err := rowAsMap(ctx, db, `SELECT * FROM items WHERE id=?`, itemID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("item %d not found", itemID)
	}
	d.Item = row
	if v, ok := row["classification_id"].(int64); ok {
		_ = db.QueryRowContext(ctx, `SELECT name FROM classifications WHERE id=?`, v).Scan(&d.Classification)
	}
	if v, ok := row["data_source_id"].(int64); ok {
		_ = db.QueryRowContext(ctx, `SELECT name FROM data_sources WHERE id=?`, v).Scan(&d.DataSource)
	}

	// owner
	if v, ok := row["attribute_id"].(int64); ok {
		if e, err := tl.debugEntityByAttribute(ctx, v); err == nil {
			d.Owner = e
		} else {
			d.Warnings = append(d.Warnings, "owner attribute "+fmt.Sprint(v)+": "+err.Error())
		}
	}

	// edges
	seenEntities := map[int64]bool{}
	for _, out := range []bool{true, false} {
		edges, err := tl.debugEdges(ctx, itemID, out)
		if err != nil {
			return nil, err
		}
		for i := range edges {
			e := &edges[i]
			if e.ItemID != nil {
				if s, err := rowAsMap(ctx, db, `SELECT id, classification_id, timestamp, data_text, data_file, data_type, original_id, metadata FROM items WHERE id=?`, *e.ItemID); err == nil && s != nil {
					if cid, ok := s["classification_id"].(int64); ok {
						var name string
						_ = db.QueryRowContext(ctx, `SELECT name FROM classifications WHERE id=?`, cid).Scan(&name)
						s["classification"] = name
					}
					d.ItemsReferenced[*e.ItemID] = s
				}
			}
			if e.EntityID != nil && !seenEntities[*e.EntityID] {
				seenEntities[*e.EntityID] = true
				if ent, err := tl.debugEntity(ctx, *e.EntityID); err == nil {
					d.Entities = append(d.Entities, *ent)
				}
			}
		}
		if out {
			d.EdgesOut = edges
		} else {
			d.EdgesIn = edges
		}
	}

	// data file
	if df, ok := row["data_file"].(string); ok && df != "" {
		f := &DebugFile{RepoRelative: df, FullPath: tl.FullPath(df), ServeURL: "/repo/" + tl.id.String() + "/" + df}
		if info, err := os.Stat(f.FullPath); err == nil {
			f.Exists = true
			f.Size = info.Size()
			f.ModTime = info.ModTime().UTC().Format(time.RFC3339)
			if fh, err := os.Open(f.FullPath); err == nil {
				h := newHash()
				_, _ = io.Copy(h, fh)
				fh.Close()
				f.BLAKE3 = hex.EncodeToString(h.Sum(nil))
				if dbHash, ok := row["data_hash"].(string); ok {
					m := dbHash == f.BLAKE3
					f.HashMatches = &m
				}
			}
		}
		d.DataFile = f

		// immich mapping
		if dbHash, ok := row["data_hash"].(string); ok {
			hb, _ := hex.DecodeString(dbHash)
			var im DebugImmich
			var uploaded int64
			var evicted *int64
			err := db.QueryRowContext(ctx, `SELECT asset_id, sha1, status, uploaded, evicted, size, data_file FROM immich_assets WHERE data_hash=?`, hb).
				Scan(&im.AssetID, &im.SHA1, &im.Status, &uploaded, &evicted, &im.Size, &im.DataFile)
			if err == nil {
				im.Uploaded = time.UnixMilli(uploaded).UTC().Format(time.RFC3339)
				if evicted != nil {
					im.Evicted = time.UnixMilli(*evicted).UTC().Format(time.RFC3339)
				}
				if tl.immich != nil {
					base := strings.TrimRight(tl.immich.opts.URL, "/")
					im.WebURL = base + "/photos/" + im.AssetID
					im.APIURL = base + "/api/assets/" + im.AssetID
					im.Original = base + "/api/assets/" + im.AssetID + "/original"
				}
				d.Immich = &im
			} else if err != sql.ErrNoRows {
				d.Warnings = append(d.Warnings, "immich lookup: "+err.Error())
			}
		}
	}

	// link-fetch cache entry (bookmarks carry the canonical URL as data_text)
	if d.Classification == "bookmark" {
		if url, ok := row["data_text"].(string); ok && url != "" {
			sum := sha1.Sum([]byte(url)) //nolint:gosec // cache key
			key := hex.EncodeToString(sum[:])
			p := filepath.Join(tl.repoDir, "linkfetch", key[:2], key, "result.json")
			if b, err := os.ReadFile(p); err == nil {
				var res map[string]any
				if json.Unmarshal(b, &res) == nil {
					res["cache_dir"] = filepath.Dir(p)
					d.LinkFetch = res
				}
			} else {
				d.LinkFetch = map[string]any{"cache_dir": filepath.Dir(p), "cached": false}
			}
		}
	}

	// job
	if v, ok := row["job_id"].(int64); ok {
		if j, err := rowAsMap(ctx, db, `SELECT id, type, state, created, start, ended, message, total, progress, parent_job_id FROM jobs WHERE id=?`, v); err == nil && j != nil {
			delete(j, "configuration")
			d.Job = j
		}
	}

	// thumbnail cache
	if df, ok := row["data_file"].(string); ok && df != "" && tl.thumbs.ReadPool != nil {
		if t, err := rowAsMap(ctx, tl.thumbs.ReadPool, `SELECT data_file, generated, length(content) AS bytes FROM thumbnails WHERE data_file=? LIMIT 1`, df); err == nil && t != nil {
			d.Thumbnail = t
		}
	}

	return d, nil
}

func (tl *Timeline) debugEdges(ctx context.Context, itemID int64, out bool) ([]DebugEdge, error) {
	col, otherItem, otherAttr := "from_item_id", "to_item_id", "to_attribute_id"
	if !out {
		col, otherItem, otherAttr = "to_item_id", "from_item_id", "from_attribute_id"
	}
	rows, err := tl.db.ReadPool.QueryContext(ctx, fmt.Sprintf(`SELECT r.id, rel.label, rel.directed, r.value, r.%s, r.%s, r.start, r."end", r.metadata
		FROM relationships r JOIN relations rel ON rel.id = r.relation_id WHERE r.%s = ? ORDER BY r.id`, otherItem, otherAttr, col), itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []DebugEdge
	for rows.Next() {
		var e DebugEdge
		var meta *string
		if err := rows.Scan(&e.ID, &e.Label, &e.Directed, &e.Value, &e.ItemID, &e.AttributeID, &e.Start, &e.End, &meta); err != nil {
			return nil, err
		}
		if b, ok := e.Value.([]byte); ok {
			e.Value = string(b)
		}
		if meta != nil {
			var m any
			if json.Unmarshal([]byte(*meta), &m) == nil {
				e.Metadata = m
			}
		}
		if e.AttributeID != nil {
			var name, value string
			var entityID *int64
			var entityName *string
			_ = tl.db.ReadPool.QueryRowContext(ctx, `SELECT a.name, coalesce(a.value,''), ea.entity_id, e.name FROM attributes a
				LEFT JOIN entity_attributes ea ON ea.attribute_id = a.id LEFT JOIN entities e ON e.id = ea.entity_id
				WHERE a.id=? LIMIT 1`, *e.AttributeID).Scan(&name, &value, &entityID, &entityName)
			e.Attribute = name + "=" + value
			e.EntityID = entityID
			if entityName != nil {
				e.Entity = *entityName
			}
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

func (tl *Timeline) debugEntityByAttribute(ctx context.Context, attrID int64) (*DebugEntity, error) {
	attr, err := rowAsMap(ctx, tl.db.ReadPool, `SELECT * FROM attributes WHERE id=?`, attrID)
	if err != nil || attr == nil {
		return nil, fmt.Errorf("attribute not found")
	}
	var entityID int64
	if err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT entity_id FROM entity_attributes WHERE attribute_id=? LIMIT 1`, attrID).Scan(&entityID); err != nil {
		return &DebugEntity{ViaAttribute: attr}, nil
	}
	e, err := tl.debugEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	e.ViaAttribute = attr
	return e, nil
}

func (tl *Timeline) debugEntity(ctx context.Context, entityID int64) (*DebugEntity, error) {
	e := &DebugEntity{ID: entityID}
	var typeID int64
	var name *string
	if err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT type_id, name FROM entities WHERE id=?`, entityID).Scan(&typeID, &name); err != nil {
		return nil, err
	}
	if name != nil {
		e.Name = *name
	}
	_ = tl.db.ReadPool.QueryRowContext(ctx, `SELECT name FROM entity_types WHERE id=?`, typeID).Scan(&e.Type)
	rows, err := tl.db.ReadPool.QueryContext(ctx, `SELECT a.*, ea.data_source_id AS via_data_source_id FROM attributes a JOIN entity_attributes ea ON ea.attribute_id = a.id WHERE ea.entity_id=? ORDER BY a.id`, entityID)
	if err != nil {
		return e, nil
	}
	defer rows.Close()
	e.Attributes, _ = rowsAsMaps(rows)
	return e, nil
}

// rowAsMap runs a single-row query and returns the row as a map (nil if no row).
func rowAsMap(ctx context.Context, db *sql.DB, query string, args ...any) (map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	maps, err := rowsAsMaps(rows)
	if err != nil || len(maps) == 0 {
		return nil, err
	}
	return maps[0], nil
}

func rowsAsMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			switch b := v.(type) {
			case []byte:
				if strings.HasSuffix(c, "hash") || strings.HasSuffix(c, "_key") || c == "hash" {
					v = hex.EncodeToString(b)
				} else if c == "metadata" || c == "configuration" {
					var j any
					if json.Unmarshal(b, &j) == nil {
						v = j
					} else {
						v = string(b)
					}
				} else {
					v = string(b)
				}
			case string:
				if c == "metadata" {
					var j any
					if json.Unmarshal([]byte(b), &j) == nil {
						v = j
					}
				}
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
