// Package meta_test imports the Meta testing fixture (built by scripts/build-testing-data.py
// from testdata/meta/cases.json) into a temporary repository with the real pipeline and
// checks that every case is represented as the manifest says.
//
//	go test ./tests/meta                                  # fresh temp repo from the fixture
//	go test ./tests/meta -run 'TestMeta/ig-msg-pseudo'    # one case
//	TLZ_TEST_REPO=/mnt/photos/timelinize/repo-dev go test ./tests/meta   # check an existing repo instead
//	TLZ_TESTDATA=/path/to/testing-data                    # fixture location (default /mnt/photos/timelinize/testing-data)
//	TLZ_TEST_KEEP=1                                       # keep the temp repo (path is logged)
package meta_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	_ "github.com/timelinize/timelinize/datasources/facebook"
	_ "github.com/timelinize/timelinize/datasources/instagram"
	"github.com/timelinize/timelinize/timeline"
)

const (
	defaultFixture = "/mnt/photos/timelinize/testing-data"
	manifestDir    = "../../testdata/meta"
)

// loadManifests merges the case manifests named by TLZ_CASES ("messages" by default, "posts",
// "all", or a comma list) — the same rule scripts/build-testing-data.py and tests/ui use.
func loadManifests(t *testing.T) manifest {
	t.Helper()
	names := os.Getenv("TLZ_CASES")
	if names == "" {
		names = "messages"
	}
	var list []string
	if names == "all" {
		entries, err := os.ReadDir(manifestDir)
		if err != nil {
			t.Fatalf("listing manifests: %v", err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				list = append(list, strings.TrimSuffix(e.Name(), ".json"))
			}
		}
	} else {
		list = strings.Split(names, ",")
	}
	var merged manifest
	seenCase, seenCheck := map[string]bool{}, map[string]bool{}
	for _, name := range list {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(manifestDir, name+".json"))
		if err != nil {
			t.Fatalf("reading manifest %s: %v", name, err)
		}
		var m manifest
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("parsing manifest %s: %v", name, err)
		}
		for _, c := range m.Cases {
			if seenCase[c.ID] {
				t.Fatalf("duplicate case id %s in %s.json", c.ID, name)
			}
			seenCase[c.ID] = true
			merged.Cases = append(merged.Cases, c)
		}
		for _, ch := range m.Checks {
			if !seenCheck[ch.ID] {
				seenCheck[ch.ID] = true
				merged.Checks = append(merged.Checks, ch)
			}
		}
	}
	t.Logf("cases: %s (%d cases, %d checks)", strings.Join(list, ","), len(merged.Cases), len(merged.Checks))
	return merged
}

// ---- manifest ------------------------------------------------------------------------------

type manifest struct {
	Cases  []testCase `json:"cases"`
	Checks []check    `json:"checks"`
}

type testCase struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Entity     string `json:"entity"`
	Subtype    string `json:"subtype"`
	Why        string `json:"why"`
	KnownIssue string `json:"known_issue"`
	Expect     struct {
		Items []itemExpect `json:"items"`
	} `json:"expect"`
}

type check struct {
	ID     string `json:"id"`
	SQL    string `json:"sql"`
	Expect int    `json:"expect"`
}

// where selects items; every set field must match.
type where struct {
	TS               *int64         `json:"ts"`
	Classification   string         `json:"classification"`
	HasText          *bool          `json:"has_text"`
	HasFile          *bool          `json:"has_file"`
	IsRoot           *bool          `json:"is_root"`
	HasLocation      *bool          `json:"has_location"` // items.latitude set
	DataTextContains string         `json:"data_text_contains"`
	DataFile         string         `json:"data_file"` // suffix
	DataFileContains string         `json:"data_file_contains"`
	DataTypePrefix   string         `json:"data_type_prefix"`
	Metadata         map[string]any `json:"metadata"` // subset, values compared as strings
}

type itemExpect struct {
	Where            where           `json:"where"`
	Count            *int            `json:"count"`
	TS               *int64          `json:"ts"`       // exact items.timestamp (ms)
	Timespan         *int64          `json:"timespan"` // exact items.timespan (ms)
	Classification   string          `json:"classification"`
	DataText         json.RawMessage `json:"data_text"` // string, or null = must be empty
	DataTextContains string          `json:"data_text_contains"`
	DataFile         json.RawMessage `json:"data_file"` // true/false, or a filename suffix
	DataTypePrefix   string          `json:"data_type_prefix"`
	HasText          *bool           `json:"has_text"`
	Owner            string          `json:"owner"` // entity name or identity attribute value
	Metadata         map[string]any  `json:"metadata"`
	MetadataHas      []string        `json:"metadata_has"`
	EdgesOut         []edgeExpect    `json:"edges_out"`
	EdgesIn          []edgeExpect    `json:"edges_in"`
	EdgesOutCount    map[string]int  `json:"edges_out_count"`
}

type edgeExpect struct {
	Label            string     `json:"label"`
	Value            string     `json:"value"`
	ToEntity         string     `json:"to_entity"`
	ToEntityContains string     `json:"to_entity_contains"`
	ToEntityType     string     `json:"to_entity_type"`
	FromEntity       string     `json:"from_entity"`
	ToItem           *itemMatch `json:"to_item"`
	FromItem         *itemMatch `json:"from_item"`
}

type itemMatch struct {
	TS               *int64 `json:"ts"`
	Timespan         *int64 `json:"timespan"`
	Classification   string `json:"classification"`
	DataText         string `json:"data_text"`
	DataTextContains string `json:"data_text_contains"`
	DataFile         string `json:"data_file"` // suffix
	DataFileContains string `json:"data_file_contains"`
}

// ---- repo access ---------------------------------------------------------------------------

type item struct {
	ID          int64
	TS          *int64
	Timespan    *int64
	Class       string
	DataText    *string
	DataFile    *string
	DataType    *string
	Metadata    map[string]any
	AttributeID *int64
	Root        bool
}

type edge struct {
	Label      string
	Value      string
	ItemID     *int64 // the other item, if any
	Entity     string // the other entity's name, if any
	EntityType string
	EntityAttr string // identity attribute value of the other entity
}

type repo struct {
	db *sql.DB
}

func openRepo(t *testing.T, dir string) *repo {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "timeline.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("opening repo db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &repo{db: db}
}

func (r *repo) items(source string, w where) ([]item, error) {
	q := `SELECT i.id, i.timestamp, i.timespan, coalesce(c.name,''), i.data_text, i.data_file, i.data_type, i.metadata, i.attribute_id,
		NOT EXISTS (SELECT 1 FROM relationships x WHERE x.to_item_id = i.id)
		FROM items i JOIN data_sources ds ON ds.id = i.data_source_id LEFT JOIN classifications c ON c.id = i.classification_id
		WHERE ds.name = ? AND i.deleted IS NULL`
	args := []any{source}
	if w.TS != nil {
		q += " AND i.timestamp = ?"
		args = append(args, *w.TS)
	}
	if w.Classification != "" {
		q += " AND c.name = ?"
		args = append(args, w.Classification)
	}
	if w.HasLocation != nil {
		if *w.HasLocation {
			q += " AND i.latitude IS NOT NULL"
		} else {
			q += " AND i.latitude IS NULL"
		}
	}
	q += " ORDER BY i.id"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var it item
		var meta *string
		if err := rows.Scan(&it.ID, &it.TS, &it.Timespan, &it.Class, &it.DataText, &it.DataFile, &it.DataType, &meta, &it.AttributeID, &it.Root); err != nil {
			return nil, err
		}
		if meta != nil {
			_ = json.Unmarshal([]byte(*meta), &it.Metadata)
		}
		if !metadataMatches(it.Metadata, w.Metadata) {
			continue
		}
		if w.HasText != nil && *w.HasText != (it.DataText != nil && *it.DataText != "") {
			continue
		}
		if w.HasFile != nil && *w.HasFile != (it.DataFile != nil) {
			continue
		}
		if w.IsRoot != nil && *w.IsRoot != it.Root {
			continue
		}
		if w.DataTextContains != "" && (it.DataText == nil || !strings.Contains(*it.DataText, w.DataTextContains)) {
			continue
		}
		if w.DataFile != "" && (it.DataFile == nil || !strings.HasSuffix(*it.DataFile, w.DataFile)) {
			continue
		}
		if w.DataFileContains != "" && (it.DataFile == nil || !strings.Contains(*it.DataFile, w.DataFileContains)) {
			continue
		}
		if w.DataTypePrefix != "" && (it.DataType == nil || !strings.HasPrefix(*it.DataType, w.DataTypePrefix)) {
			continue
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *repo) owner(attrID *int64) (name, attrValue string) {
	if attrID == nil {
		return "", ""
	}
	_ = r.db.QueryRow(`SELECT coalesce(e.name,''), coalesce(a.value,'') FROM attributes a
		LEFT JOIN entity_attributes ea ON ea.attribute_id = a.id LEFT JOIN entities e ON e.id = ea.entity_id
		WHERE a.id = ? LIMIT 1`, *attrID).Scan(&name, &attrValue)
	return
}

func (r *repo) edges(itemID int64, out bool) ([]edge, error) {
	col, other, otherAttr := "from_item_id", "to_item_id", "to_attribute_id"
	if !out {
		col, other, otherAttr = "to_item_id", "from_item_id", "from_attribute_id"
	}
	rows, err := r.db.Query(fmt.Sprintf(`SELECT rel.label, coalesce(r.value,''), r.%s, r.%s FROM relationships r
		JOIN relations rel ON rel.id = r.relation_id WHERE r.%s = ?`, other, otherAttr, col), itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []edge
	for rows.Next() {
		var e edge
		var attrID *int64
		var val any
		if err := rows.Scan(&e.Label, &val, &e.ItemID, &attrID); err != nil {
			return nil, err
		}
		e.Value = fmt.Sprint(val)
		if attrID != nil {
			_ = r.db.QueryRow(`SELECT coalesce(e.name,''), coalesce(et.name,''), coalesce(a.value,'') FROM attributes a
				LEFT JOIN entity_attributes ea ON ea.attribute_id = a.id LEFT JOIN entities e ON e.id = ea.entity_id
				LEFT JOIN entity_types et ON et.id = e.type_id WHERE a.id = ? LIMIT 1`, *attrID).Scan(&e.Entity, &e.EntityType, &e.EntityAttr)
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

func (r *repo) itemByID(id int64) (item, error) {
	var it item
	var meta *string
	err := r.db.QueryRow(`SELECT i.id, i.timestamp, i.timespan, coalesce(c.name,''), i.data_text, i.data_file, i.data_type, i.metadata, i.attribute_id
		FROM items i LEFT JOIN classifications c ON c.id = i.classification_id WHERE i.id = ?`, id).
		Scan(&it.ID, &it.TS, &it.Timespan, &it.Class, &it.DataText, &it.DataFile, &it.DataType, &meta, &it.AttributeID)
	if meta != nil {
		_ = json.Unmarshal([]byte(*meta), &it.Metadata)
	}
	return it, err
}

// ---- assertions ----------------------------------------------------------------------------

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (it item) String() string {
	ts := ""
	if it.TS != nil {
		ts = time.UnixMilli(*it.TS).UTC().Format(time.RFC3339)
	}
	if it.Timespan != nil {
		ts += "→" + time.UnixMilli(*it.Timespan).UTC().Format(time.RFC3339)
	}
	txt := str(it.DataText)
	if len(txt) > 50 {
		txt = txt[:50] + "…"
	}
	return fmt.Sprintf("item %d [%s %s] text=%q file=%q type=%q root=%v", it.ID, it.Class, ts, txt, str(it.DataFile), str(it.DataType), it.Root)
}

func matchItem(r *repo, m *itemMatch, id *int64) bool {
	if m == nil {
		return true
	}
	if id == nil {
		return false
	}
	it, err := r.itemByID(*id)
	if err != nil {
		return false
	}
	if m.Classification != "" && it.Class != m.Classification {
		return false
	}
	if m.TS != nil && (it.TS == nil || *it.TS != *m.TS) {
		return false
	}
	if m.Timespan != nil && (it.Timespan == nil || *it.Timespan != *m.Timespan) {
		return false
	}
	if m.DataText != "" && str(it.DataText) != m.DataText {
		return false
	}
	if m.DataTextContains != "" && !strings.Contains(str(it.DataText), m.DataTextContains) {
		return false
	}
	if m.DataFile != "" && !strings.HasSuffix(str(it.DataFile), m.DataFile) {
		return false
	}
	if m.DataFileContains != "" && !strings.Contains(str(it.DataFile), m.DataFileContains) {
		return false
	}
	return true
}

func matchEdge(r *repo, want edgeExpect, e edge, out bool) bool {
	if want.Label != "" && e.Label != want.Label {
		return false
	}
	if want.Value != "" && e.Value != want.Value {
		return false
	}
	entityWant := want.ToEntity
	if !out {
		entityWant = want.FromEntity
	}
	if entityWant != "" && e.Entity != entityWant && e.EntityAttr != entityWant {
		return false
	}
	if want.ToEntityContains != "" && !strings.Contains(e.Entity, want.ToEntityContains) {
		return false
	}
	if want.ToEntityType != "" && e.EntityType != want.ToEntityType {
		return false
	}
	if out && !matchItem(r, want.ToItem, e.ItemID) {
		return false
	}
	if !out && !matchItem(r, want.FromItem, e.ItemID) {
		return false
	}
	return true
}

func describeEdges(r *repo, edges []edge) string {
	var parts []string
	for _, e := range edges {
		s := e.Label
		if e.Value != "" {
			s += "(" + e.Value + ")"
		}
		if e.ItemID != nil {
			if it, err := r.itemByID(*e.ItemID); err == nil {
				s += " -> " + it.String()
			}
		}
		if e.Entity != "" {
			s += " -> entity " + e.Entity + " [" + e.EntityType + "]"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "; ")
}

func checkItem(t *testing.T, r *repo, it item, x itemExpect) {
	t.Helper()
	if x.Classification != "" && it.Class != x.Classification {
		t.Errorf("classification: want %q, got %s", x.Classification, it)
	}
	if x.TS != nil && (it.TS == nil || *it.TS != *x.TS) {
		t.Errorf("timestamp: want %d (%s), got %s", *x.TS, time.UnixMilli(*x.TS).UTC().Format(time.RFC3339), it)
	}
	if x.Timespan != nil && (it.Timespan == nil || *it.Timespan != *x.Timespan) {
		t.Errorf("timespan: want %d (%s), got %s", *x.Timespan, time.UnixMilli(*x.Timespan).UTC().Format(time.RFC3339), it)
	}
	if len(x.DataText) > 0 {
		if string(x.DataText) == "null" {
			if str(it.DataText) != "" {
				t.Errorf("data_text: want empty, got %s", it)
			}
		} else {
			var want string
			_ = json.Unmarshal(x.DataText, &want)
			if str(it.DataText) != want {
				t.Errorf("data_text: want %q, got %s", want, it)
			}
		}
	}
	if x.DataTextContains != "" && !strings.Contains(str(it.DataText), x.DataTextContains) {
		t.Errorf("data_text should contain %q: %s", x.DataTextContains, it)
	}
	if x.HasText != nil && *x.HasText != (str(it.DataText) != "") {
		t.Errorf("has_text: want %v: %s", *x.HasText, it)
	}
	if len(x.DataFile) > 0 {
		var b bool
		var s string
		switch {
		case json.Unmarshal(x.DataFile, &b) == nil:
			if b != (it.DataFile != nil) {
				t.Errorf("data_file present: want %v: %s", b, it)
			}
		case json.Unmarshal(x.DataFile, &s) == nil:
			if !strings.HasSuffix(str(it.DataFile), s) {
				t.Errorf("data_file should end with %q: %s", s, it)
			}
		}
	}
	if x.DataTypePrefix != "" && !strings.HasPrefix(str(it.DataType), x.DataTypePrefix) {
		t.Errorf("data_type should start with %q: %s", x.DataTypePrefix, it)
	}
	if x.Owner != "" {
		name, attr := r.owner(it.AttributeID)
		if name != x.Owner && attr != x.Owner {
			t.Errorf("owner: want %q, got entity %q / attribute %q: %s", x.Owner, name, attr, it)
		}
	}
	for k, v := range x.Metadata {
		got, ok := it.Metadata[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("metadata[%s]: want %v, got %v (have keys %v): %s", k, v, got, keys(it.Metadata), it)
		}
	}
	for _, k := range x.MetadataHas {
		if _, ok := it.Metadata[k]; !ok {
			t.Errorf("metadata should have key %q (have %v): %s", k, keys(it.Metadata), it)
		}
	}
	if len(x.EdgesOut) > 0 || len(x.EdgesOutCount) > 0 {
		edges, err := r.edges(it.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range x.EdgesOut {
			found := false
			for _, e := range edges {
				if matchEdge(r, want, e, true) {
					found = true
					break
				}
			}
			if !found {
				wj, _ := json.Marshal(want)
				t.Errorf("missing outgoing edge %s on %s\n    have: %s", wj, it, describeEdges(r, edges))
			}
		}
		for label, n := range x.EdgesOutCount {
			c := 0
			for _, e := range edges {
				if e.Label == label {
					c++
				}
			}
			if c != n {
				t.Errorf("outgoing %q edges: want %d, got %d on %s\n    have: %s", label, n, c, it, describeEdges(r, edges))
			}
		}
	}
	if len(x.EdgesIn) > 0 {
		edges, err := r.edges(it.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range x.EdgesIn {
			found := false
			for _, e := range edges {
				if matchEdge(r, want, e, false) {
					found = true
					break
				}
			}
			if !found {
				wj, _ := json.Marshal(want)
				t.Errorf("missing incoming edge %s on %s\n    have: %s", wj, it, describeEdges(r, edges))
			}
		}
	}
}

func metadataMatches(have map[string]any, want map[string]any) bool {
	for k, v := range want {
		got, ok := have[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- fixture import ------------------------------------------------------------------------

func fixtureDir() string {
	if d := os.Getenv("TLZ_TESTDATA"); d != "" {
		return d
	}
	return defaultFixture
}

// importFixture creates a temp repo and imports both sources of the fixture with the real pipeline.
func importFixture(t *testing.T) string {
	t.Helper()
	fixture := fixtureDir()
	if _, err := os.Stat(filepath.Join(fixture, "instagram")); err != nil {
		t.Skipf("fixture not found at %s (run scripts/build-testing-data.py); set TLZ_TESTDATA or TLZ_TEST_REPO", fixture)
	}
	dir := t.TempDir()
	if os.Getenv("TLZ_TEST_KEEP") != "" {
		dir, _ = os.MkdirTemp("", "tlz-meta-test-")
		t.Logf("keeping temp repo at %s", dir)
	}
	ctx := context.Background()
	tl, err := timeline.Create(ctx, dir)
	if err != nil {
		t.Fatalf("creating temp repo: %v", err)
	}
	t.Cleanup(func() { tl.Close() })

	constraints := map[string]bool{"filename": true, "timestamp": true, "latlon": true, "classification_name": true, "data": true}
	// import roots: the two exports, plus the separate E2EE Messenger export when the fixture has one
	roots := [][2]string{{"instagram", filepath.Join(fixture, "instagram")}, {"facebook", filepath.Join(fixture, "facebook", "data")}}
	if _, err := os.Stat(filepath.Join(fixture, "facebook", "data_messenger_e2e")); err == nil {
		roots = append(roots, [2]string{"facebook", filepath.Join(fixture, "facebook", "data_messenger_e2e")})
	}
	var jobIDs []uint64
	for _, sr := range roots {
		src, root := sr[0], sr[1]
		if _, err := os.Stat(root); err != nil {
			continue // a manifest selection may leave a source empty
		}
		job := &timeline.ImportJob{
			Plan:              timeline.ImportPlan{Files: []timeline.FileImport{{DataSourceName: src, Filenames: []string{root}}}},
			ProcessingOptions: timeline.ProcessingOptions{ItemUniqueConstraints: constraints},
		}
		id, err := tl.CreateJob(job, time.Time{}, 0, 0, 0)
		if err != nil {
			t.Fatalf("creating %s import job: %v", src, err)
		}
		jobIDs = append(jobIDs, id)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for len(jobIDs) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("import jobs did not finish in time: %v", jobIDs)
		}
		time.Sleep(500 * time.Millisecond)
		jobs, err := tl.GetJobs(ctx, jobIDs, 0)
		if err != nil {
			t.Fatalf("querying jobs: %v", err)
		}
		var pending []uint64
		for _, j := range jobs {
			switch j.State {
			case timeline.JobSucceeded:
				t.Logf("import job %d succeeded: %s", j.ID, str(j.Message))
			case timeline.JobFailed, timeline.JobAborted:
				t.Fatalf("import job %d ended with state %s: %s", j.ID, j.State, str(j.Message))
			default:
				pending = append(pending, j.ID)
			}
		}
		jobIDs = pending
	}
	return dir
}

// ---- the test ------------------------------------------------------------------------------

func TestMeta(t *testing.T) {
	m := loadManifests(t)

	dir := os.Getenv("TLZ_TEST_REPO")
	if dir == "" {
		dir = importFixture(t)
	} else {
		t.Logf("checking existing repo %s", dir)
	}
	r := openRepo(t, dir)

	for _, c := range m.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			if c.KnownIssue != "" {
				t.Logf("known issue: %s (expectations describe current behaviour)", c.KnownIssue)
			}
			for i, x := range c.Expect.Items {
				items, err := r.items(c.Source, x.Where)
				if err != nil {
					t.Fatalf("querying items: %v", err)
				}
				wj, _ := json.Marshal(x.Where)
				if x.Count != nil {
					if len(items) != *x.Count {
						var got []string
						for _, it := range items {
							got = append(got, it.String())
						}
						t.Errorf("expect[%d] where %s: want %d item(s), got %d: %s", i, wj, *x.Count, len(items), strings.Join(got, " | "))
						continue
					}
				} else if len(items) == 0 {
					t.Errorf("expect[%d] where %s: no items matched", i, wj)
					continue
				}
				for _, it := range items {
					checkItem(t, r, it, x)
				}
			}
		})
	}

	t.Run("checks", func(t *testing.T) {
		for _, ch := range m.Checks {
			var n int
			if err := r.db.QueryRow(ch.SQL).Scan(&n); err != nil {
				t.Errorf("%s: %v", ch.ID, err)
				continue
			}
			if n != ch.Expect {
				t.Errorf("%s: want %d, got %d", ch.ID, ch.Expect, n)
			}
		}
	})
}
