package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sequenceCloudflaredHTTPDoer struct {
	mu        sync.Mutex
	responses []*http.Response
	requests  []*http.Request
}

func (d *sequenceCloudflaredHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, request)
	if len(d.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	response := d.responses[0]
	d.responses = d.responses[1:]
	if response.Request == nil {
		response.Request = request
	}
	return response, nil
}

func cloudflaredTestResponse(status int, payload []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
	}
}

func cloudflaredTestRelease(t *testing.T, tag, assetName string, payload []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(payload)
	release := cloudflaredRelease{
		TagName: tag,
		Assets: []cloudflaredReleaseAsset{{
			Name:               assetName,
			Size:               int64(len(payload)),
			Digest:             "sha256:" + hex.EncodeToString(sum[:]),
			BrowserDownloadURL: "https://github.com/cloudflare/cloudflared/releases/download/" + tag + "/" + assetName,
		}},
	}
	data, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCloudflaredTargetForSupportedPlatforms(t *testing.T) {
	tests := []struct {
		goos      string
		goarch    string
		assetName string
		tarGzip   bool
		ok        bool
	}{
		{goos: "windows", goarch: "amd64", assetName: "cloudflared-windows-amd64.exe", ok: true},
		{goos: "linux", goarch: "amd64", assetName: "cloudflared-linux-amd64", ok: true},
		{goos: "linux", goarch: "arm64", assetName: "cloudflared-linux-arm64", ok: true},
		{goos: "darwin", goarch: "amd64", assetName: "cloudflared-darwin-amd64.tgz", tarGzip: true, ok: true},
		{goos: "darwin", goarch: "arm64", assetName: "cloudflared-darwin-arm64.tgz", tarGzip: true, ok: true},
		{goos: "windows", goarch: "arm64", ok: false},
	}
	for _, test := range tests {
		t.Run(test.goos+"-"+test.goarch, func(t *testing.T) {
			target, ok := cloudflaredTargetFor(test.goos, test.goarch)
			if ok != test.ok || target.assetName != test.assetName || target.tarGzip != test.tarGzip {
				t.Fatalf("target=%+v ok=%v", target, ok)
			}
		})
	}
}

func TestFetchCloudflaredReleaseAssetValidatesMetadata(t *testing.T) {
	payload := []byte("verified-cloudflared")
	assetName := "cloudflared-windows-amd64.exe"
	valid := cloudflaredTestRelease(t, "2026.7.2", assetName, payload)
	client := &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, valid)}}
	asset, err := fetchCloudflaredReleaseAsset(context.Background(), client, cloudflaredLatestReleaseURL, assetName)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != assetName || asset.Size != int64(len(payload)) {
		t.Fatalf("asset=%+v", asset)
	}
	if len(client.requests) != 1 || client.requests[0].Header.Get("X-GitHub-Api-Version") == "" {
		t.Fatalf("requests=%d headers=%v", len(client.requests), client.requests[0].Header)
	}

	var release cloudflaredRelease
	if err := json.Unmarshal(valid, &release); err != nil {
		t.Fatal(err)
	}
	release.Assets[0].Digest = "sha256:bad"
	invalidDigest, _ := json.Marshal(release)
	_, err = fetchCloudflaredReleaseAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, invalidDigest)}}, cloudflaredLatestReleaseURL, assetName)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum rejection, got %v", err)
	}

	if err := json.Unmarshal(valid, &release); err != nil {
		t.Fatal(err)
	}
	release.Assets[0].BrowserDownloadURL = "https://example.com/cloudflared.exe"
	invalidURL, _ := json.Marshal(release)
	_, err = fetchCloudflaredReleaseAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, invalidURL)}}, cloudflaredLatestReleaseURL, assetName)
	if !errors.Is(err, errCloudflaredDownloadDenied) {
		t.Fatalf("expected URL rejection, got %v", err)
	}

	if err := json.Unmarshal(valid, &release); err != nil {
		t.Fatal(err)
	}
	release.Assets[0].BrowserDownloadURL = "https://github.com:8443/cloudflare/cloudflared/releases/download/2026.7.2/" + assetName
	invalidPort, _ := json.Marshal(release)
	_, err = fetchCloudflaredReleaseAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, invalidPort)}}, cloudflaredLatestReleaseURL, assetName)
	if !errors.Is(err, errCloudflaredDownloadDenied) {
		t.Fatalf("expected non-HTTPS-port rejection, got %v", err)
	}

	if err := json.Unmarshal(valid, &release); err != nil {
		t.Fatal(err)
	}
	release.Assets = append(release.Assets, release.Assets[0])
	duplicate, _ := json.Marshal(release)
	_, err = fetchCloudflaredReleaseAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, duplicate)}}, cloudflaredLatestReleaseURL, assetName)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
}

func TestDownloadCloudflaredAssetVerifiesDigestAndLength(t *testing.T) {
	payload := []byte("portable-binary")
	sum := sha256.Sum256(payload)
	asset := cloudflaredReleaseAsset{
		Name:               "cloudflared-windows-amd64.exe",
		Size:               int64(len(payload)),
		Digest:             "sha256:" + hex.EncodeToString(sum[:]),
		BrowserDownloadURL: "https://github.com/cloudflare/cloudflared/releases/download/2026.7.2/cloudflared-windows-amd64.exe",
	}
	destination, err := os.CreateTemp(t.TempDir(), "download-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := downloadCloudflaredAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, payload)}}, asset, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded=%q err=%v", got, err)
	}

	asset.Digest = "sha256:" + strings.Repeat("0", sha256.Size*2)
	if err := downloadCloudflaredAsset(context.Background(), &sequenceCloudflaredHTTPDoer{responses: []*http.Response{cloudflaredTestResponse(http.StatusOK, payload)}}, asset, destination); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func writeCloudflaredTarGzip(t *testing.T, entries []tar.Header, payloads [][]byte) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "cloudflared.tgz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for index := range entries {
		header := entries[index]
		payload := payloads[index]
		regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		if regular {
			header.Size = int64(len(payload))
		}
		if err := archive.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if regular && len(payload) > 0 {
			if _, err := archive.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestExtractCloudflaredTarGzipAcceptsOnlyExpectedExecutable(t *testing.T) {
	payload := []byte("darwin-cloudflared")
	archivePath := writeCloudflaredTarGzip(t, []tar.Header{{Name: "cloudflared", Typeflag: tar.TypeReg, Mode: 0o755}}, [][]byte{payload})
	destination, err := os.CreateTemp(t.TempDir(), "extracted-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := extractCloudflaredTarGzip(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("extracted=%q err=%v", got, err)
	}

	for name, header := range map[string]tar.Header{
		"traversal": {Name: "../cloudflared", Typeflag: tar.TypeReg, Mode: 0o755},
		"symlink":   {Name: "cloudflared", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			badArchive := writeCloudflaredTarGzip(t, []tar.Header{header}, [][]byte{payload})
			output, err := os.CreateTemp(t.TempDir(), "bad-*.tmp")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if err := extractCloudflaredTarGzip(badArchive, output); err == nil {
				t.Fatal("expected unsafe archive rejection")
			}
		})
	}
}

func TestGitHubCloudflaredInstallerInstallsVerifiedPortableBinary(t *testing.T) {
	payload := []byte("verified-portable-cloudflared")
	assetName := "cloudflared-windows-amd64.exe"
	client := &sequenceCloudflaredHTTPDoer{responses: []*http.Response{
		cloudflaredTestResponse(http.StatusOK, cloudflaredTestRelease(t, "2026.7.2", assetName, payload)),
		cloudflaredTestResponse(http.StatusOK, payload),
	}}
	installer := &githubCloudflaredInstaller{
		homeDir:    t.TempDir(),
		goos:       "windows",
		goarch:     "amd64",
		releaseURL: cloudflaredLatestReleaseURL,
		client:     client,
	}
	installedPath, err := installer.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if installedPath != installer.ManagedPath() || filepath.Base(installedPath) != "cloudflared.exe" {
		t.Fatalf("installed path=%q", installedPath)
	}
	installed, err := os.ReadFile(installedPath)
	if err != nil || !bytes.Equal(installed, payload) {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
	entries, err := os.ReadDir(filepath.Dir(installedPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cloudflared.exe" {
		t.Fatalf("unexpected managed directory entries: %+v", entries)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
}

func TestGitHubCloudflaredInstallerCleansUpFailedChecksum(t *testing.T) {
	metadataPayload := []byte("expected-cloudflared")
	downloadPayload := []byte("tampered-cloudflared")
	if len(metadataPayload) != len(downloadPayload) {
		t.Fatal("fixture lengths must match")
	}
	assetName := "cloudflared-windows-amd64.exe"
	installer := &githubCloudflaredInstaller{
		homeDir:    t.TempDir(),
		goos:       "windows",
		goarch:     "amd64",
		releaseURL: cloudflaredLatestReleaseURL,
		client: &sequenceCloudflaredHTTPDoer{responses: []*http.Response{
			cloudflaredTestResponse(http.StatusOK, cloudflaredTestRelease(t, "2026.7.2", assetName, metadataPayload)),
			cloudflaredTestResponse(http.StatusOK, downloadPayload),
		}},
	}
	_, err := installer.Install(context.Background())
	if !errors.Is(err, errCloudflaredInstallFailed) {
		t.Fatalf("expected install failure, got %v", err)
	}
	if _, statErr := os.Stat(installer.ManagedPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("managed binary should not exist: %v", statErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(installer.ManagedPath()))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files were not cleaned up: %+v", entries)
	}
}

type fakeCloudflaredInstaller struct {
	supported bool
	path      string
	started   chan struct{}
	release   <-chan struct{}
	err       error
	once      sync.Once
}

func (i *fakeCloudflaredInstaller) Supported() bool { return i != nil && i.supported }
func (i *fakeCloudflaredInstaller) ManagedPath() string {
	if i == nil {
		return ""
	}
	return i.path
}
func (i *fakeCloudflaredInstaller) Install(ctx context.Context) (string, error) {
	if i.started != nil {
		i.once.Do(func() { close(i.started) })
	}
	if i.release != nil {
		select {
		case <-i.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if i.err != nil {
		return "", i.err
	}
	if err := os.MkdirAll(filepath.Dir(i.path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(i.path, []byte("fake-cloudflared"), 0o700); err != nil {
		return "", err
	}
	return i.path, nil
}

func TestTemporaryTunnelManagerInstallsThenStartsManagedBinary(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "bin", "cloudflared")
	installer := &fakeCloudflaredInstaller{supported: true, path: managedPath}
	process := newFakeTemporaryTunnelProcess()
	var commandName string
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		installer: installer,
		command: func(_ context.Context, name string, _ ...string) temporaryTunnelProcess {
			commandName = name
			return process
		},
		startTimeout: time.Second,
	})
	before := manager.Snapshot()
	if before.Available || !before.Installable || before.Status != temporaryTunnelUnavailable {
		t.Fatalf("before=%+v", before)
	}
	installed, err := manager.InstallCloudflared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Available || installed.Installable || installed.Status != temporaryTunnelIdle {
		t.Fatalf("installed=%+v", installed)
	}
	running, err := manager.StartTunnel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != temporaryTunnelRunning || commandName != managedPath {
		t.Fatalf("running=%+v command=%q", running, commandName)
	}
	if _, err := manager.StopTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTemporaryTunnelManagerSerializesInstallation(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "bin", "cloudflared")
	started := make(chan struct{})
	release := make(chan struct{})
	installer := &fakeCloudflaredInstaller{supported: true, path: managedPath, started: started, release: release}
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		installer: installer,
	})
	type result struct {
		snapshot TemporaryTunnelSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, err := manager.InstallCloudflared(context.Background())
		done <- result{snapshot: snapshot, err: err}
	}()
	<-started
	installing := manager.Snapshot()
	if installing.Status != temporaryTunnelInstalling || installing.Available || !installing.Installable || installing.Error != "" {
		t.Fatalf("installing=%+v", installing)
	}
	stopped, err := manager.StopTunnel(context.Background())
	if err != nil || stopped.Status != temporaryTunnelInstalling {
		t.Fatalf("stop during install changed state: snapshot=%+v err=%v", stopped, err)
	}
	if _, err := manager.InstallCloudflared(context.Background()); !errors.Is(err, errCloudflaredInstallInProgress) {
		t.Fatalf("expected concurrent install rejection, got %v", err)
	}
	close(release)
	completed := <-done
	if completed.err != nil || !completed.snapshot.Available || completed.snapshot.Status != temporaryTunnelIdle {
		t.Fatalf("completed=%+v err=%v", completed.snapshot, completed.err)
	}
}

func TestTemporaryTunnelManagerDetectsManagedBinaryAfterRestart(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "bin", "cloudflared")
	if err := os.MkdirAll(filepath.Dir(managedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedPath, []byte("existing-cloudflared"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		installer: &fakeCloudflaredInstaller{supported: false, path: managedPath},
	})
	snapshot := manager.Snapshot()
	if !snapshot.Available || snapshot.Installable || snapshot.Status != temporaryTunnelIdle {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
