// Command modelcatalog writes or checks an offline model metadata snapshot.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-gang/modelcatalog"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "modelcatalog:", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("modelcatalog", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "models.json", "catalog JSON destination")
	manifest := flags.String("manifest", "snapshot.json", "snapshot manifest destination")
	check := flags.Bool("check", false, "verify both destinations without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	outputPath, err := destination(*output)
	if err != nil || *output == "" {
		return fmt.Errorf("invalid output path")
	}
	manifestPath, err := destination(*manifest)
	if err != nil || *manifest == "" || outputPath == manifestPath {
		return fmt.Errorf("manifest and catalog must have distinct nonempty paths")
	}
	outputInfo, outputErr := os.Stat(outputPath)
	manifestInfo, manifestErr := os.Stat(manifestPath)
	if outputErr == nil && manifestErr == nil && os.SameFile(outputInfo, manifestInfo) {
		return fmt.Errorf("manifest and catalog refer to the same file")
	}
	catalogJSON, manifestJSON, err := modelcatalog.Generate()
	if err != nil {
		return err
	}
	files := []struct {
		path string
		data []byte
	}{{outputPath, catalogJSON}, {manifestPath, manifestJSON}}
	if *check {
		var failures []error
		for _, file := range files {
			data, err := os.ReadFile(file.path)
			if err != nil {
				failures = append(failures, fmt.Errorf("check %s: %w", file.path, err))
			} else if !bytes.Equal(data, file.data) {
				failures = append(failures, fmt.Errorf("%s is stale; run modelcatalog without -check", file.path))
			}
		}
		return errors.Join(failures...)
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	return nil
}

func destination(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(resolved)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return filepath.EvalSymlinks(resolved)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info != nil && info.IsDir() {
		return "", fmt.Errorf("destination is a directory")
	}
	return resolved, nil
}
