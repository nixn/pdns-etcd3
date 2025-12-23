//go:build integration

/* Copyright 2016-2025 nix <https://keybase.io/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package src

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// getCacheDir returns a cache dir for etcd downloads.
func getCacheDir() string {
	if d := os.Getenv("ETCD_CACHE"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "etcd")
	}
	return filepath.Join(os.TempDir(), "etcd-cache")
}

// fetchEtcdBinaryCached downloads and extracts only if not already cached.
// Returns path to etcd binary.
//
//goland:noinspection GoUnhandledErrorResult
func fetchEtcdBinaryCached(t *testing.T, version string) string {
	t.Helper()
	// honor override to skip entirely
	if p := os.Getenv("ETCD_BINARY"); p != "" {
		return p
	}

	cache := getCacheDir()
	dest := filepath.Join(cache, "etcd-"+version)
	// expected binary inside extracted tree
	expected := filepath.Join(dest, "etcd")
	if runtime.GOOS == "windows" {
		expected += ".exe"
	}
	// quick check if already present and executable
	if fi, err := os.Stat(expected); err == nil && !fi.IsDir() {
		return expected
	}

	// create tmp dir for extraction first (avoid half-extracted state)
	tmpRoot := filepath.Join(cache, fmt.Sprintf("etcd-%s-tmp", version))
	_ = os.RemoveAll(tmpRoot)
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}

	// download URL
	osName := runtime.GOOS
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "amd64"
	} else if arch == "arm64" {
		arch = "arm64"
	}
	filename := fmt.Sprintf("etcd-v%s-%s-%s.tar.gz", version, osName, arch)
	url := fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/v%s/%s", version, filename)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bad status: %s", res.Status)
	}

	gzr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	var topLevel string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		name := filepath.Clean(hdr.Name)
		if topLevel == "" {
			parts := strings.SplitN(name, string(filepath.Separator), 2)
			topLevel = parts[0]
		}
		target := filepath.Join(tmpRoot, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("mkdir parent: %v", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				t.Fatalf("copy: %v", err)
			}
			f.Close()
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil && runtime.GOOS != "windows" {
				t.Fatalf("symlink: %v", err)
			}
		}
	}

	// move tmp extraction to final cache location atomically
	finalTop := filepath.Join(tmpRoot, topLevel)
	if err := os.RemoveAll(dest); err != nil {
		t.Fatalf("rm dest: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir dest parent: %v", err)
	}
	if err := os.Rename(finalTop, dest); err != nil {
		// fallback: copy
		if err := os.Rename(tmpRoot, dest); err != nil {
			t.Fatalf("move to cache: %v", err)
		}
	}

	// expected binary path inside extracted dir
	etcdPath := filepath.Join(dest, "etcd")
	if runtime.GOOS == "windows" {
		etcdPath += ".exe"
	}
	// ensure executable
	if err := os.Chmod(etcdPath, 0o755); err != nil {
		t.Logf("chmod: %v", err)
	}
	return etcdPath
}

func getGitVersion(t *testing.T) string {
	t.Helper()
	v := "???"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				v = setting.Value
			case "vcs.modified":
				if setting.Value == "true" {
					v += "*"
				}
			}
		}
	}
	return v
}
