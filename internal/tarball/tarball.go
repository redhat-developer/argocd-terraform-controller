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

// Package tarball creates and extracts gzip-compressed tar archives of
// source directories, guarding against path traversal and decompression
// bombs.
package tarball

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	securejoin "github.com/cyphar/filepath-securejoin"
)

// DefaultMaxSize caps the extracted size at 1GiB.
const DefaultMaxSize = 1 << 30

var epoch = time.Unix(0, 0)

// Archive tars and gzips dir. Paths in the archive are relative to dir.
// Entries for which skip returns true are omitted; skip may be nil.
func Archive(dir string, skip func(rel string, info fs.FileInfo) bool) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if skip != nil && skip(rel, info) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks are stored as links; everything else that is not a regular
		// file or directory is skipped.
		var link string
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !d.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		// Strip ownership and times for reproducible archives.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		hdr.ModTime, hdr.AccessTime, hdr.ChangeTime = epoch, epoch, epoch
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Extract untars the gzip archive in r into dst. It refuses entries that
// escape dst and stops once maxSize bytes have been written (0 means
// DefaultMaxSize).
func Extract(r io.Reader, dst string, maxSize int64) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("invalid gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()
	return ExtractTar(gr, dst, maxSize)
}

// ExtractTar is Extract for an uncompressed tar stream.
func ExtractTar(r io.Reader, dst string, maxSize int64) error {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(r)

	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Name == "" || strings.HasPrefix(hdr.Name, "/") {
			continue
		}
		target, err := securejoin.SecureJoin(dst, hdr.Name)
		if err != nil {
			return fmt.Errorf("unsafe path %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := fs.FileMode(hdr.Mode).Perm() | 0o600
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxSize-written+1))
			_ = f.Close()
			if err != nil {
				return err
			}
			written += n
			if written > maxSize {
				return fmt.Errorf("archive exceeds maximum size of %d bytes", maxSize)
			}
		case tar.TypeSymlink:
			// Only allow links that stay inside dst.
			linkTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
			if _, err := securejoin.SecureJoin(dst, strings.TrimPrefix(linkTarget, dst)); err != nil || !strings.HasPrefix(filepath.Clean(linkTarget), filepath.Clean(dst)) {
				continue
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Skip devices, fifos, etc.
		}
	}
}
