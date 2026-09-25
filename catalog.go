// Package modelcatalog generates a versioned model catalog from embedded,
// reviewed public metadata. Generation is deterministic and does not use the
// network, credentials, the current date, or files in the working directory.
package modelcatalog

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/go-gang/modelcatalog/internal/cataloggen"
	"github.com/go-gang/modelcatalog/internal/modelcatalog"
)

//go:embed source/*.json
var sources embed.FS

// Manifest identifies an exact catalog snapshot. CatalogSHA256 binds the
// metadata to the generated JSON bytes, including their final newline.
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	Revision      string   `json:"revision"`
	Profile       string   `json:"profile"`
	RetrievedDate string   `json:"retrieved_date"`
	Sources       []string `json:"sources"`
	CatalogSHA256 string   `json:"catalog_sha256,omitempty"`
}

// Generate returns the catalog JSON and its manifest. The bytes belong to the
// caller and can be written, embedded, or checked against a committed snapshot.
func Generate() (catalogJSON, manifestJSON []byte, err error) {
	return generate(sources)
}

func generate(input fs.FS) ([]byte, []byte, error) {
	read := func(name string) ([]byte, error) {
		data, err := fs.ReadFile(input, "source/"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return data, nil
	}
	snapshotJSON, err := read("snapshot.json")
	if err != nil {
		return nil, nil, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(snapshotJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, fmt.Errorf("decode snapshot metadata: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("snapshot metadata must contain one JSON document")
	}
	if manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.Revision) == "" || manifest.Profile != "text-streaming-functions-v1" || len(manifest.Sources) == 0 || manifest.CatalogSHA256 != "" {
		return nil, nil, fmt.Errorf("invalid source snapshot metadata")
	}
	if _, err := time.Parse(time.DateOnly, manifest.RetrievedDate); err != nil {
		return nil, nil, fmt.Errorf("invalid snapshot retrieval date: %w", err)
	}
	for _, source := range manifest.Sources {
		if !strings.HasPrefix(source, "https://") {
			return nil, nil, fmt.Errorf("snapshot source must be a public HTTPS URL")
		}
	}
	reviewedJSON, err := read("reviewed.json")
	if err != nil {
		return nil, nil, err
	}
	reviewed, err := modelcatalog.DecodeJSON(reviewedJSON, "reviewed")
	if err != nil {
		return nil, nil, err
	}
	openrouterJSON, err := read("openrouter.json")
	if err != nil {
		return nil, nil, err
	}
	reviewedProvenance, err := read("reviewed.provenance.json")
	if err != nil {
		return nil, nil, err
	}
	openrouterProvenance, err := read("openrouter.provenance.json")
	if err != nil {
		return nil, nil, err
	}
	if err := validateProvenance(manifest, reviewed, reviewedJSON, openrouterJSON, reviewedProvenance, openrouterProvenance); err != nil {
		return nil, nil, err
	}
	catalogJSON, _, err := cataloggen.Build(openrouterJSON, reviewed, manifest.RetrievedDate, manifest.Revision)
	if err != nil {
		return nil, nil, err
	}
	manifest.CatalogSHA256 = digest(catalogJSON)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return catalogJSON, append(manifestJSON, '\n'), nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateProvenance(manifest Manifest, reviewed modelcatalog.Layer, reviewedJSON, openrouterJSON, reviewedJSONProvenance, openrouterJSONProvenance []byte) error {
	var native struct {
		SchemaVersion    int    `json:"schema_version"`
		SnapshotRevision string `json:"snapshot_revision"`
		CheckedDate      string `json:"checked_date"`
		ReviewedSHA256   string `json:"reviewed_sha256"`
		CatalogProfile   struct {
			ID string `json:"id"`
		} `json:"catalog_profile"`
		Providers map[string]struct {
			ModelCount int `json:"model_count"`
			Models     []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
		OpenRouterReviewed struct {
			ID string `json:"id"`
		} `json:"openrouter_reviewed"`
	}
	if err := json.Unmarshal(reviewedJSONProvenance, &native); err != nil {
		return fmt.Errorf("decode reviewed provenance: %w", err)
	}
	if native.SchemaVersion != 1 || native.SnapshotRevision != manifest.Revision || native.CheckedDate != manifest.RetrievedDate || native.ReviewedSHA256 != digest(reviewedJSON) || native.CatalogProfile.ID != manifest.Profile {
		return fmt.Errorf("reviewed source does not match its provenance")
	}
	ids := map[string]bool{}
	for provider, details := range native.Providers {
		if provider == "" || provider == modelcatalog.ProviderOpenRouter || details.ModelCount != len(details.Models) {
			return fmt.Errorf("invalid provenance provider %q", provider)
		}
		for _, entry := range details.Models {
			key := provider + "/" + entry.ID
			if entry.ID == "" || ids[key] {
				return fmt.Errorf("empty or duplicate provenance ID %q", key)
			}
			ids[key] = true
		}
	}
	if native.OpenRouterReviewed.ID != "" {
		ids[modelcatalog.ProviderOpenRouter+"/"+native.OpenRouterReviewed.ID] = true
	}
	for _, entry := range reviewed.Models {
		key := entry.Provider + "/" + entry.ID
		if !ids[key] {
			return fmt.Errorf("reviewed model has no provenance: %s", key)
		}
		delete(ids, key)
	}
	if len(ids) != 0 {
		return fmt.Errorf("provenance contains models absent from reviewed data")
	}
	var router struct {
		SnapshotRevision  string `json:"snapshot_revision"`
		NormalizedSHA256  string `json:"normalized_sha256"`
		SourceURL         string `json:"source_url"`
		AuthorizationUsed *bool  `json:"authorization_used"`
	}
	if err := json.Unmarshal(openrouterJSONProvenance, &router); err != nil {
		return fmt.Errorf("decode OpenRouter provenance: %w", err)
	}
	if router.SnapshotRevision != manifest.Revision || router.NormalizedSHA256 != digest(openrouterJSON) || router.SourceURL != cataloggen.OpenRouterSource || router.AuthorizationUsed == nil || *router.AuthorizationUsed {
		return fmt.Errorf("OpenRouter source does not match its public provenance")
	}
	return nil
}
