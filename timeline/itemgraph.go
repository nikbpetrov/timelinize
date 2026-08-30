package timeline

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The relationship graph around an item, for the item page's graph view (fork):
// a breadth-first walk over the relationships table from a seed item, returning
// item and entity nodes with the edges between them. Entities are reached through
// attributes (owner attribute of an item, or the attribute at the far end of an
// edge) and are not expanded further — that would pull in a person's whole history.

// GraphView is the result of ItemGraph.
type GraphView struct {
	Seed  string      `json:"seed"` // node key of the seed item, "item:<id>"
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Truncated is set when the node limit stopped the walk.
	Truncated bool `json:"truncated,omitempty"`
}

// GraphNode is an item or an entity.
type GraphNode struct {
	Key  string `json:"key"`  // "item:<id>" or "entity:<id>"
	Kind string `json:"kind"` // "item" or "entity"
	ID   int64  `json:"id"`

	// items
	Classification string  `json:"classification,omitempty"`
	DataSource     string  `json:"data_source,omitempty"`
	Timestamp      *int64  `json:"timestamp,omitempty"` // ms
	Text           string  `json:"text,omitempty"`      // first 120 chars of data_text
	DataType       string  `json:"data_type,omitempty"`
	DataFile       string  `json:"data_file,omitempty"`
	Metadata       *string `json:"metadata,omitempty"` // raw JSON
	Depth          int     `json:"depth"`              // hops from the seed

	// entities
	Type    string `json:"type,omitempty"`
	Name    string `json:"name,omitempty"`
	Picture string `json:"picture,omitempty"`
	Owner   bool   `json:"owner,omitempty"` // entity 1
}

// GraphEdge is a relationship between two nodes.
type GraphEdge struct {
	ID       int64  `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Label    string `json:"label"`
	Directed bool   `json:"directed"`
	Value    any    `json:"value,omitempty"`
	Start    *int64 `json:"start,omitempty"`
}

// ItemGraph walks up to depth hops of item→item relationships away from itemID
// (default 2, max 4) and returns at most maxNodes nodes (default 150).
func (tl *Timeline) ItemGraph(ctx context.Context, itemID int64, depth, maxNodes int) (*GraphView, error) {
	if depth <= 0 {
		depth = 2
	}
	if depth > 4 {
		depth = 4
	}
	if maxNodes <= 0 {
		maxNodes = 150
	}
	g := &GraphView{Seed: fmt.Sprintf("item:%d", itemID)}
	nodes := make(map[string]*GraphNode)
	edgesSeen := make(map[int64]bool)

	addItem := func(id int64, d int) (*GraphNode, error) {
		key := fmt.Sprintf("item:%d", id)
		if n, ok := nodes[key]; ok {
			return n, nil
		}
		n, err := tl.graphItemNode(ctx, id)
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, nil
		}
		n.Depth = d
		nodes[key] = n
		g.Nodes = append(g.Nodes, *n)
		return n, nil
	}
	addEntityByAttr := func(attrID int64) (string, error) {
		var entityID int64
		var name, typ, picture sql.NullString
		err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT e.id, e.name, et.name, e.picture_file FROM entity_attributes ea
			JOIN entities e ON e.id = ea.entity_id LEFT JOIN entity_types et ON et.id = e.type_id WHERE ea.attribute_id=? LIMIT 1`, attrID).
			Scan(&entityID, &name, &typ, &picture)
		if err != nil {
			return "", nil // attribute without an entity: nothing to draw
		}
		key := fmt.Sprintf("entity:%d", entityID)
		if _, ok := nodes[key]; !ok {
			n := &GraphNode{Key: key, Kind: "entity", ID: entityID, Name: name.String, Type: typ.String, Picture: picture.String, Owner: entityID == ownerEntityID}
			nodes[key] = n
			g.Nodes = append(g.Nodes, *n)
		}
		return key, nil
	}
	// the owner of an item is an implicit edge worth drawing
	ownerEdge := func(n *GraphNode, attrID *int64) error {
		if attrID == nil {
			return nil
		}
		key, err := addEntityByAttr(*attrID)
		if err != nil || key == "" {
			return err
		}
		g.Edges = append(g.Edges, GraphEdge{From: n.Key, To: key, Label: "owner", Directed: true})
		return nil
	}

	type queued struct {
		id    int64
		depth int
	}
	seedNode, err := addItem(itemID, 0)
	if err != nil {
		return nil, err
	}
	if seedNode == nil {
		return nil, fmt.Errorf("item %d not found", itemID)
	}
	if err := ownerEdge(seedNode, tl.graphItemOwnerAttr(ctx, itemID)); err != nil {
		return nil, err
	}
	queue := []queued{{itemID, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			continue
		}
		rows, err := tl.db.ReadPool.QueryContext(ctx, `SELECT r.id, rel.label, rel.directed, r.value, r.from_item_id, r.to_item_id, r.from_attribute_id, r.to_attribute_id, r.start
			FROM relationships r JOIN relations rel ON rel.id = r.relation_id
			WHERE r.from_item_id = ? OR r.to_item_id = ? ORDER BY r.id`, cur.id, cur.id)
		if err != nil {
			return nil, err
		}
		type rawEdge struct {
			id               int64
			label            string
			directed         bool
			value            any
			fromItem, toItem *int64
			fromAttr, toAttr *int64
			start            *int64
		}
		var raw []rawEdge
		for rows.Next() {
			var e rawEdge
			if err := rows.Scan(&e.id, &e.label, &e.directed, &e.value, &e.fromItem, &e.toItem, &e.fromAttr, &e.toAttr, &e.start); err != nil {
				rows.Close()
				return nil, err
			}
			if b, ok := e.value.([]byte); ok {
				e.value = string(b)
			}
			raw = append(raw, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, e := range raw {
			if edgesSeen[e.id] {
				continue
			}
			if len(nodes) >= maxNodes {
				g.Truncated = true
				break
			}
			var fromKey, toKey string
			switch {
			case e.fromItem != nil:
				n, err := addItem(*e.fromItem, cur.depth+1)
				if err != nil {
					return nil, err
				}
				if n == nil {
					continue
				}
				fromKey = n.Key
				if *e.fromItem != cur.id {
					if err := ownerEdge(n, tl.graphItemOwnerAttr(ctx, *e.fromItem)); err != nil {
						return nil, err
					}
					queue = append(queue, queued{*e.fromItem, cur.depth + 1})
				}
			case e.fromAttr != nil:
				k, err := addEntityByAttr(*e.fromAttr)
				if err != nil {
					return nil, err
				}
				fromKey = k
			}
			switch {
			case e.toItem != nil:
				n, err := addItem(*e.toItem, cur.depth+1)
				if err != nil {
					return nil, err
				}
				if n == nil {
					continue
				}
				toKey = n.Key
				if *e.toItem != cur.id {
					if err := ownerEdge(n, tl.graphItemOwnerAttr(ctx, *e.toItem)); err != nil {
						return nil, err
					}
					queue = append(queue, queued{*e.toItem, cur.depth + 1})
				}
			case e.toAttr != nil:
				k, err := addEntityByAttr(*e.toAttr)
				if err != nil {
					return nil, err
				}
				toKey = k
			}
			if fromKey == "" || toKey == "" {
				continue
			}
			edgesSeen[e.id] = true
			g.Edges = append(g.Edges, GraphEdge{ID: e.id, From: fromKey, To: toKey, Label: e.label, Directed: e.directed, Value: e.value, Start: e.start})
		}
		if g.Truncated {
			break
		}
	}
	// nodes were appended as copies before Depth was final for some; refresh from the map
	for i := range g.Nodes {
		if n, ok := nodes[g.Nodes[i].Key]; ok {
			g.Nodes[i] = *n
		}
	}
	return g, nil
}

func (tl *Timeline) graphItemOwnerAttr(ctx context.Context, itemID int64) *int64 {
	var attr *int64
	_ = tl.db.ReadPool.QueryRowContext(ctx, `SELECT attribute_id FROM items WHERE id=?`, itemID).Scan(&attr)
	return attr
}

func (tl *Timeline) graphItemNode(ctx context.Context, id int64) (*GraphNode, error) {
	n := &GraphNode{Key: fmt.Sprintf("item:%d", id), Kind: "item", ID: id}
	var class, ds, text, dtype, dfile, meta sql.NullString
	err := tl.db.ReadPool.QueryRowContext(ctx, `SELECT c.name, ds.name, i.timestamp, i.data_text, i.data_type, i.data_file, i.metadata
		FROM items i LEFT JOIN classifications c ON c.id = i.classification_id LEFT JOIN data_sources ds ON ds.id = i.data_source_id
		WHERE i.id=? AND i.deleted IS NULL`, id).Scan(&class, &ds, &n.Timestamp, &text, &dtype, &dfile, &meta)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.Classification, n.DataSource, n.DataType, n.DataFile = class.String, ds.String, dtype.String, dfile.String
	if text.Valid {
		t := strings.TrimSpace(text.String)
		if len(t) > 120 {
			t = t[:120] + "…"
		}
		n.Text = t
	}
	if meta.Valid {
		m := meta.String
		n.Metadata = &m
	}
	return n, nil
}
