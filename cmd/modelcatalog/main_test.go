package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gang/modelcatalog"
)

func TestGenerateAndCheckOutsideSourceCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := run(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-check"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	catalogJSON, manifestJSON, err := modelcatalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"models.json": catalogJSON, "snapshot.json": manifestJSON} {
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s differs from embedded snapshot: %v", name, err)
		}
	}
}

func TestCheckDoesNotRepairOrCreateFiles(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "missing"}[missing], func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := run(nil, io.Discard); err != nil {
				t.Fatal(err)
			}
			originalManifest, err := os.ReadFile("snapshot.json")
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				err = os.Remove("models.json")
			} else {
				err = os.WriteFile("models.json", []byte("user content\n"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"-check"}, io.Discard); err == nil {
				t.Fatal("check accepted a stale or missing catalog")
			}
			catalogJSON, err := os.ReadFile("models.json")
			if missing {
				if !os.IsNotExist(err) {
					t.Fatalf("check created missing file: %v", err)
				}
			} else if err != nil || string(catalogJSON) != "user content\n" {
				t.Fatalf("check overwrote stale file: %v", err)
			}
			manifestJSON, err := os.ReadFile("snapshot.json")
			if err != nil || !bytes.Equal(manifestJSON, originalManifest) {
				t.Fatalf("check modified the manifest: %v", err)
			}
		})
	}
}

func TestExplicitDestinationsAndArgumentValidation(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("generated", 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"-output", filepath.Join("generated", "catalog.json"), "-manifest", filepath.Join("generated", "metadata.json")}
	if err := run(args, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "-check"), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"extra"}, {"-unknown"}, {"-output", ""}, {"-manifest", ""}, {"-output", "same", "-manifest", "./same"}} {
		if err := run(args, io.Discard); err == nil {
			t.Fatalf("invalid arguments accepted: %q", args)
		}
	}
}

func TestAliasedDestinationsAreRejectedWithoutWriting(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink", "parent symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.Mkdir("real", 0o755); err != nil {
				t.Fatal(err)
			}
			original := []byte("existing user data\n")
			if err := os.WriteFile("real/catalog.json", original, 0o644); err != nil {
				t.Fatal(err)
			}
			alias := "alias.json"
			var err error
			switch kind {
			case "hardlink":
				err = os.Link("real/catalog.json", alias)
			case "symlink":
				err = os.Symlink("real/catalog.json", alias)
			case "parent symlink":
				err = os.Symlink("real", "alias")
				alias = "alias/catalog.json"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"-output", "real/catalog.json", "-manifest", alias}, io.Discard); err == nil {
				t.Fatal("accepted two destinations pointing at one file")
			}
			got, err := os.ReadFile("real/catalog.json")
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("argument rejection modified user file: %v", err)
			}
		})
	}
}
