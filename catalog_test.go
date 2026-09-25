package modelcatalog

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/go-gang/modelcatalog/internal/cataloggen"
	"github.com/go-gang/modelcatalog/internal/modelcatalog"
)

func TestGenerateReproducesCommittedSnapshotAndReviewedRecords(t *testing.T) {
	catalogJSON, manifestJSON, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	again, againManifest, err := Generate()
	if err != nil || !bytes.Equal(catalogJSON, again) || !bytes.Equal(manifestJSON, againManifest) {
		t.Fatalf("generation is not deterministic: %v", err)
	}
	for name, want := range map[string][]byte{"models.json": catalogJSON, "snapshot.json": manifestJSON} {
		committed, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(committed, want) {
			t.Fatalf("%s is stale; run make generate", name)
		}
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.CatalogSHA256 != digest(catalogJSON) {
		t.Fatal("manifest is not bound to generated catalog bytes")
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := sources.ReadFile("source/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	reviewed, err := modelcatalog.DecodeJSON(read("reviewed.json"), "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := modelcatalog.DecodeJSON(catalogJSON, "generated")
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range reviewed.Models {
		index := slices.IndexFunc(generated.Models, func(entry modelcatalog.Entry) bool {
			return entry.Provider == original.Provider && entry.ID == original.ID
		})
		if index < 0 || !reflect.DeepEqual(generated.Models[index], original) {
			t.Fatalf("reviewed record changed: %s/%s", original.Provider, original.ID)
		}
	}
	_, report, err := cataloggen.Build(read("openrouter.json"), reviewed, manifest.RetrievedDate, manifest.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Included) != 283 || len(report.Excluded["batch_api"]) != 72 || len(report.Excluded["dynamic_router"]) != 19 {
		t.Fatalf("pinned OpenRouter selection changed: %d included, exclusions=%v", len(report.Included), report.Excluded)
	}
}

func sourceFixture(t *testing.T) fstest.MapFS {
	t.Helper()
	fixture := fstest.MapFS{}
	entries, err := sources.ReadDir("source")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := "source/" + entry.Name()
		data, err := fs.ReadFile(sources, name)
		if err != nil {
			t.Fatal(err)
		}
		fixture[name] = &fstest.MapFile{Data: bytes.Clone(data)}
	}
	return fixture
}

func alterJSON(t *testing.T, fixture fstest.MapFS, name string, change func(map[string]any)) {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(fixture["source/"+name].Data, &data); err != nil {
		t.Fatal(err)
	}
	change(data)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	fixture["source/"+name].Data = encoded
}

func TestGenerateRejectsBrokenSourceAndProvenance(t *testing.T) {
	changes := map[string]func(*testing.T, fstest.MapFS){
		"missing input": func(_ *testing.T, files fstest.MapFS) { delete(files, "source/openrouter.json") },
		"reviewed hash": func(_ *testing.T, files fstest.MapFS) {
			files["source/reviewed.json"].Data = append(files["source/reviewed.json"].Data, '\n')
		},
		"router hash": func(_ *testing.T, files fstest.MapFS) {
			files["source/openrouter.json"].Data = append(files["source/openrouter.json"].Data, '\n')
		},
		"malformed provenance": func(_ *testing.T, files fstest.MapFS) {
			files["source/reviewed.provenance.json"].Data = []byte("{")
		},
		"invalid snapshot": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "snapshot.json", func(data map[string]any) { data["schema_version"] = 2 })
		},
		"unsupported profile": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "snapshot.json", func(data map[string]any) { data["profile"] = "unknown-profile" })
		},
		"mismatched profile": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) {
				data["catalog_profile"].(map[string]any)["id"] = "different-profile"
			})
		},
		"invalid date": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "snapshot.json", func(data map[string]any) { data["retrieved_date"] = "today" })
		},
		"unknown snapshot key": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "snapshot.json", func(data map[string]any) { data["typo"] = true })
		},
		"reviewed revision": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) { data["snapshot_revision"] = "different" })
		},
		"router revision": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "openrouter.provenance.json", func(data map[string]any) { data["snapshot_revision"] = "different" })
		},
		"duplicate provenance ID": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) {
				provider := data["providers"].(map[string]any)["openai"].(map[string]any)
				models := provider["models"].([]any)
				provider["models"] = append(models, models[0])
				provider["model_count"] = len(models) + 1
			})
		},
		"missing provenance ID": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) {
				provider := data["providers"].(map[string]any)["openai"].(map[string]any)
				models := provider["models"].([]any)
				provider["models"] = models[1:]
				provider["model_count"] = len(models) - 1
			})
		},
		"unmatched provenance ID": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) {
				provider := data["providers"].(map[string]any)["openai"].(map[string]any)
				models := provider["models"].([]any)
				provider["models"] = append(models, map[string]any{"id": "extra-model"})
				provider["model_count"] = len(models) + 1
			})
		},
		"private source": func(t *testing.T, files fstest.MapFS) {
			alterJSON(t, files, "openrouter.provenance.json", func(data map[string]any) { data["authorization_used"] = true })
		},
		"malformed reviewed source": func(t *testing.T, files fstest.MapFS) {
			files["source/reviewed.json"].Data = []byte(`{"version":1,"models":[],"typo":true}`)
			alterJSON(t, files, "reviewed.provenance.json", func(data map[string]any) {
				data["reviewed_sha256"] = digest(files["source/reviewed.json"].Data)
			})
		},
		"malformed router source": func(t *testing.T, files fstest.MapFS) {
			files["source/openrouter.json"].Data = []byte(`{"data":[]}`)
			alterJSON(t, files, "openrouter.provenance.json", func(data map[string]any) {
				data["normalized_sha256"] = digest(files["source/openrouter.json"].Data)
			})
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			files := sourceFixture(t)
			change(t, files)
			catalogJSON, manifestJSON, err := generate(files)
			if err == nil || catalogJSON != nil || manifestJSON != nil {
				t.Fatal("broken source produced a catalog")
			}
		})
	}
}
