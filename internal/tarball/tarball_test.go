/*
Copyright 2026 The Argo CD Terraform Controller Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tarball

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveExtractRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.tf"), `resource "terraform_data" "x" {}`)
	writeFile(t, filepath.Join(src, "modules", "a", "main.tf"), "# module a")
	writeFile(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(src, "secret.tfvars"), "x = 1")

	skip := func(rel string, _ fs.FileInfo) bool {
		return rel == ".git" || strings.HasSuffix(rel, ".tfvars")
	}
	data, err := Archive(src, skip)
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := Extract(bytes.NewReader(data), dst, 0); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"main.tf", "modules/a/main.tf"} {
		if _, err := os.Stat(filepath.Join(dst, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	for _, unwanted := range []string{".git/HEAD", "secret.tfvars"} {
		if _, err := os.Stat(filepath.Join(dst, unwanted)); err == nil {
			t.Errorf("%s should have been skipped", unwanted)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dst, "main.tf"))
	if string(got) != `resource "terraform_data" "x" {}` {
		t.Errorf("content = %q", got)
	}
}

func TestArchiveIsReproducible(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.tf"), "a")
	first, err := Archive(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Touch the file so mtime changes but content does not.
	if err := os.Chtimes(filepath.Join(src, "a.tf"), epoch.Add(1e9), epoch.Add(1e9)); err != nil {
		t.Fatal(err)
	}
	second, err := Archive(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("archives differ for identical content")
	}
}

func makeArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func TestExtractRejectsTraversal(t *testing.T) {
	dst := t.TempDir()
	data := makeArchive(t, map[string]string{"../../escape.txt": "pwned", "ok.txt": "fine"})
	if err := Extract(bytes.NewReader(data), dst, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dst)), "escape.txt")); err == nil {
		t.Fatal("path traversal succeeded")
	}
	// securejoin keeps the entry inside dst instead.
	if _, err := os.Stat(filepath.Join(dst, "escape.txt")); err != nil {
		t.Errorf("expected traversal entry to be contained in dst: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "ok.txt")); err != nil {
		t.Errorf("ok.txt missing: %v", err)
	}
}

func TestExtractEnforcesMaxSize(t *testing.T) {
	dst := t.TempDir()
	data := makeArchive(t, map[string]string{"big.txt": strings.Repeat("x", 2048)})
	if err := Extract(bytes.NewReader(data), dst, 1024); err == nil {
		t.Fatal("expected size limit error")
	}
}
