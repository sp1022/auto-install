package deploy

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRuntimeArtifact_FlatPatroniFilesBecomeBinLayout(t *testing.T) {
	sourceDir := t.TempDir()
	for _, path := range []string{
		filepath.Join(sourceDir, "python3"),
		filepath.Join(sourceDir, "patroni"),
		filepath.Join(sourceDir, "patronictl"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0755); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}
	sitePackages := filepath.Join(sourceDir, "site-packages")
	if err := os.MkdirAll(sitePackages, 0755); err != nil {
		t.Fatalf("failed to create site-packages dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitePackages, "dummy.py"), []byte("x=1"), 0644); err != nil {
		t.Fatalf("failed to create dummy python module: %v", err)
	}

	artifact, cleanup, err := prepareRuntimeArtifact(sourceDir, "patroni")
	if err != nil {
		t.Fatalf("prepareRuntimeArtifact failed: %v", err)
	}
	defer cleanup()

	entries := readTarGzEntries(t, artifact)
	assertTarEntry(t, entries, "bin/python3")
	assertTarEntry(t, entries, "bin/patroni")
	assertTarEntry(t, entries, "bin/patronictl")
	assertTarEntry(t, entries, "lib/site-packages/dummy.py")
}

func TestPrepareRuntimeArtifact_FlatEtcdFilesBecomeBinLayout(t *testing.T) {
	sourceDir := t.TempDir()
	for _, path := range []string{
		filepath.Join(sourceDir, "etcd"),
		filepath.Join(sourceDir, "etcdctl"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0755); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	artifact, cleanup, err := prepareRuntimeArtifact(sourceDir, "etcd")
	if err != nil {
		t.Fatalf("prepareRuntimeArtifact failed: %v", err)
	}
	defer cleanup()

	entries := readTarGzEntries(t, artifact)
	assertTarEntry(t, entries, "bin/etcd")
	assertTarEntry(t, entries, "bin/etcdctl")
}

func TestNormalizeEtcdRuntimeShell_ContainsFallbackDiscovery(t *testing.T) {
	cmd := normalizeEtcdRuntimeShell("/opt/pg-deploy/etcd-runtime")
	for _, expected := range []string{
		"mkdir -p '/opt/pg-deploy/etcd-runtime'/bin",
		"find '/opt/pg-deploy/etcd-runtime' -maxdepth 3 -type f -name etcd",
		"install -m 755 \"$found_etcd\" '/opt/pg-deploy/etcd-runtime'/bin/etcd",
		"find '/opt/pg-deploy/etcd-runtime' -maxdepth 3 -type f -name etcdctl",
		"install -m 755 \"$found_etcdctl\" '/opt/pg-deploy/etcd-runtime'/bin/etcdctl",
	} {
		if !strings.Contains(cmd, expected) {
			t.Fatalf("expected shell snippet %q in %s", expected, cmd)
		}
	}
}

func TestValidatePatroniRuntimeShell_ChecksEtcdImplementations(t *testing.T) {
	cmd := validatePatroniRuntimeShell("/opt/pg-deploy/patroni-runtime")
	for _, expected := range []string{
		"'/opt/pg-deploy/patroni-runtime'/bin/python3",
		"import patroni, patroni.dcs.etcd, patroni.dcs.etcd3",
	} {
		if !strings.Contains(cmd, expected) {
			t.Fatalf("expected shell snippet %q in %s", expected, cmd)
		}
	}
}

func readTarGzEntries(t *testing.T, archivePath string) map[string]bool {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open archive %s: %v", archivePath, err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	entries := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		entries[header.Name] = true
	}
	return entries
}

func assertTarEntry(t *testing.T, entries map[string]bool, name string) {
	t.Helper()
	if !entries[name] {
		t.Fatalf("expected tar entry %s, got %#v", name, entries)
	}
}
