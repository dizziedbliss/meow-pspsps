// Package catalog holds a compact snapshot of the node types and credential types available in the
// judge's pinned n8n image. `judge bake` regenerates catalog.json from a live container.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed catalog.json
var embedded []byte

// NodeCred is a credential a node type can use.
type NodeCred struct {
	Name        string `json:"name"`
	Required    bool   `json:"required,omitempty"`
	Conditional bool   `json:"conditional,omitempty"` // only shown for some parameter values
}

// NodeType lists the versions of a node type the image knows, and its credentials.
type NodeType struct {
	DisplayName string     `json:"displayName"`
	Versions    []float64  `json:"versions"`
	Creds       []NodeCred `json:"creds,omitempty"`
}

// CredField is one input a team fills in for a credential.
type CredField struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Required    bool   `json:"required,omitempty"`
	Password    bool   `json:"password,omitempty"`
	Default     string `json:"default,omitempty"`
}

// CredType is a credential type and the fields the judge asks for.
type CredType struct {
	DisplayName string      `json:"displayName"`
	Fields      []CredField `json:"fields"`
	OAuth       bool        `json:"oauth,omitempty"`
}

// Catalog is the whole snapshot.
type Catalog struct {
	N8nVersion string               `json:"n8nVersion"`
	Nodes      map[string]*NodeType `json:"nodes"`
	Creds      map[string]*CredType `json:"creds"`
}

var (
	once   sync.Once
	loaded *Catalog
	errLd  error
)

// Default returns the embedded catalog.
func Default() (*Catalog, error) {
	once.Do(func() {
		loaded = &Catalog{}
		errLd = json.Unmarshal(embedded, loaded)
		if errLd == nil && len(loaded.Nodes) == 0 {
			errLd = fmt.Errorf("embedded catalog is empty; run `judge bake` first")
		}
	})
	return loaded, errLd
}

// HasVersion reports whether the image knows this node type at this version.
func (c *Catalog) HasVersion(nodeType string, v float64) (known bool, versionOK bool) {
	nt, ok := c.Nodes[nodeType]
	if !ok {
		return false, false
	}
	for _, have := range nt.Versions {
		if have == v {
			return true, true
		}
	}
	return true, false
}

// Build turns n8n's /types/nodes.json and /types/credentials.json into a compact catalog.
func Build(nodesJSON, credsJSON []byte, version string) (*Catalog, error) {
	var nodes []struct {
		Name        string          `json:"name"`
		DisplayName string          `json:"displayName"`
		Version     json.RawMessage `json:"version"`
		Credentials []struct {
			Name           string          `json:"name"`
			Required       bool            `json:"required"`
			DisplayOptions json.RawMessage `json:"displayOptions"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("nodes.json: %w", err)
	}
	var creds []struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"displayName"`
		Extends     []string `json:"extends"`
		Properties  []struct {
			Name           string          `json:"name"`
			DisplayName    string          `json:"displayName"`
			Type           string          `json:"type"`
			Required       bool            `json:"required"`
			Default        any             `json:"default"`
			DisplayOptions json.RawMessage `json:"displayOptions"`
			TypeOptions    struct {
				Password bool `json:"password"`
			} `json:"typeOptions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(credsJSON, &creds); err != nil {
		return nil, fmt.Errorf("credentials.json: %w", err)
	}

	cat := &Catalog{N8nVersion: version, Nodes: map[string]*NodeType{}, Creds: map[string]*CredType{}}
	for _, n := range nodes {
		nt := cat.Nodes[n.Name]
		if nt == nil {
			nt = &NodeType{DisplayName: n.DisplayName}
			cat.Nodes[n.Name] = nt
		}
		var one float64
		var many []float64
		if json.Unmarshal(n.Version, &many) == nil {
			nt.Versions = append(nt.Versions, many...)
		} else if json.Unmarshal(n.Version, &one) == nil {
			nt.Versions = append(nt.Versions, one)
		}
		for _, c := range n.Credentials {
			nc := NodeCred{Name: c.Name, Required: c.Required, Conditional: len(c.DisplayOptions) > 0 && string(c.DisplayOptions) != "null"}
			dup := false
			for _, have := range nt.Creds {
				if have.Name == nc.Name {
					dup = true
				}
			}
			if !dup {
				nt.Creds = append(nt.Creds, nc)
			}
		}
	}
	for _, nt := range cat.Nodes {
		sort.Float64s(nt.Versions)
	}

	for _, c := range creds {
		ct := &CredType{DisplayName: c.DisplayName}
		for _, e := range c.Extends {
			if strings.Contains(strings.ToLower(e), "oauth") {
				ct.OAuth = true
			}
		}
		if strings.Contains(strings.ToLower(c.Name), "oauth") {
			ct.OAuth = true
		}
		for _, p := range c.Properties {
			if p.Type != "string" && p.Type != "number" {
				continue
			}
			if len(p.DisplayOptions) > 0 && string(p.DisplayOptions) != "null" {
				continue
			}
			if p.Name == "allowedDomains" {
				continue
			}
			f := CredField{Name: p.Name, DisplayName: p.DisplayName, Required: p.Required, Password: p.TypeOptions.Password}
			if p.Default != nil {
				if s := fmt.Sprint(p.Default); s != "" {
					f.Default = s
				}
			}
			ct.Fields = append(ct.Fields, f)
		}
		cat.Creds[c.Name] = ct
	}
	return cat, nil
}
