package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"autoto/internal/network"
)

const (
	cloudflaredLatestReleaseURL = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
	cloudflaredMetadataMaxBytes = 2 << 20
	cloudflaredAssetMaxBytes    = 128 << 20
	cloudflaredRequestTimeout   = 2 * time.Minute
)

var (
	errCloudflaredInstallUnsupported = errors.New("automatic cloudflared installation is not supported on this platform")
	errCloudflaredInstallInProgress  = errors.New("cloudflared installation is already in progress")
	errCloudflaredInstallFailed      = errors.New("cloudflared installation failed")
	errCloudflaredDownloadDenied     = errors.New("cloudflared download destination is not allowed")
)

type temporaryTunnelInstaller interface {
	Supported() bool
	ManagedPath() string
	Install(context.Context) (string, error)
}

type cloudflaredHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type cloudflaredInstallTarget struct {
	assetName string
	tarGzip   bool
}

type cloudflaredReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type cloudflaredRelease struct {
	TagName    string                    `json:"tag_name"`
	Draft      bool                      `json:"draft"`
	Prerelease bool                      `json:"prerelease"`
	Assets     []cloudflaredReleaseAsset `json:"assets"`
}

type githubCloudflaredInstaller struct {
	homeDir    string
	goos       string
	goarch     string
	releaseURL string
	client     cloudflaredHTTPDoer
}

func newGitHubCloudflaredInstaller(homeDir string) *githubCloudflaredInstaller {
	return &githubCloudflaredInstaller{
		homeDir:    strings.TrimSpace(homeDir),
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		releaseURL: cloudflaredLatestReleaseURL,
		client:     newCloudflaredHTTPClient(),
	}
}

func newCloudflaredHTTPClient() *http.Client {
	redirectPolicy := network.RedirectPolicy(network.PolicyPublicDirect, network.WithRedirectLimit(5))
	return &http.Client{
		Transport: network.NewPublicDirectTransport(),
		Timeout:   cloudflaredRequestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if err := redirectPolicy(request, via); err != nil {
				return err
			}
			if request == nil || !allowedCloudflaredHTTPURL(request.URL) {
				return errCloudflaredDownloadDenied
			}
			return nil
		},
	}
}

func (i *githubCloudflaredInstaller) Supported() bool {
	if i == nil || strings.TrimSpace(i.homeDir) == "" {
		return false
	}
	_, ok := cloudflaredTargetFor(i.goos, i.goarch)
	return ok
}

func (i *githubCloudflaredInstaller) ManagedPath() string {
	if i == nil || strings.TrimSpace(i.homeDir) == "" {
		return ""
	}
	name := temporaryTunnelBinary
	if strings.EqualFold(strings.TrimSpace(i.goos), "windows") {
		name += ".exe"
	}
	return filepath.Join(i.homeDir, "bin", name)
}

func (i *githubCloudflaredInstaller) Install(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	target, ok := cloudflaredTargetFor(i.goos, i.goarch)
	if !ok || strings.TrimSpace(i.homeDir) == "" {
		return "", errCloudflaredInstallUnsupported
	}
	if i.client == nil {
		return "", cloudflaredInstallFailure("the download client is unavailable")
	}

	asset, err := fetchCloudflaredReleaseAsset(ctx, i.client, i.releaseURL, target.assetName)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return "", cloudflaredInstallFailure("release metadata could not be verified")
	}

	destination := i.ManagedPath()
	binDir, err := prepareCloudflaredBinDir(i.homeDir)
	if err != nil {
		return "", cloudflaredInstallFailure("the managed binary directory could not be prepared")
	}
	archiveFile, err := os.CreateTemp(binDir, ".cloudflared-download-*.tmp")
	if err != nil {
		return "", cloudflaredInstallFailure("the release asset could not be staged")
	}
	archivePath := archiveFile.Name()
	keepArchive := false
	defer func() {
		_ = archiveFile.Close()
		if !keepArchive {
			_ = os.Remove(archivePath)
		}
	}()

	if err := downloadCloudflaredAsset(ctx, i.client, asset, archiveFile); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return "", cloudflaredInstallFailure("the release asset could not be downloaded and verified")
	}
	if err := archiveFile.Close(); err != nil {
		return "", cloudflaredInstallFailure("the verified release asset could not be finalized")
	}

	stagedPath := archivePath
	if target.tarGzip {
		executable, createErr := os.CreateTemp(binDir, ".cloudflared-install-*.tmp")
		if createErr != nil {
			return "", cloudflaredInstallFailure("the executable could not be staged")
		}
		executablePath := executable.Name()
		keepExecutable := false
		defer func() {
			_ = executable.Close()
			if !keepExecutable {
				_ = os.Remove(executablePath)
			}
		}()
		if err := extractCloudflaredTarGzip(archivePath, executable); err != nil {
			return "", cloudflaredInstallFailure("the release archive could not be verified")
		}
		if err := executable.Sync(); err != nil {
			return "", cloudflaredInstallFailure("the executable could not be finalized")
		}
		if err := executable.Close(); err != nil {
			return "", cloudflaredInstallFailure("the executable could not be finalized")
		}
		stagedPath = executablePath
		if err := commitCloudflaredBinary(stagedPath, destination, i.goos); err != nil {
			return "", cloudflaredInstallFailure("the managed executable could not be installed")
		}
		keepExecutable = true
		return destination, nil
	}

	if err := commitCloudflaredBinary(stagedPath, destination, i.goos); err != nil {
		return "", cloudflaredInstallFailure("the managed executable could not be installed")
	}
	keepArchive = true
	return destination, nil
}

func cloudflaredInstallFailure(stage string) error {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return errCloudflaredInstallFailed
	}
	return fmt.Errorf("%w: %s", errCloudflaredInstallFailed, stage)
}

func cloudflaredTargetFor(goos, goarch string) (cloudflaredInstallTarget, bool) {
	switch strings.ToLower(strings.TrimSpace(goos)) + "/" + strings.ToLower(strings.TrimSpace(goarch)) {
	case "windows/amd64":
		return cloudflaredInstallTarget{assetName: "cloudflared-windows-amd64.exe"}, true
	case "linux/amd64":
		return cloudflaredInstallTarget{assetName: "cloudflared-linux-amd64"}, true
	case "linux/arm64":
		return cloudflaredInstallTarget{assetName: "cloudflared-linux-arm64"}, true
	case "darwin/amd64":
		return cloudflaredInstallTarget{assetName: "cloudflared-darwin-amd64.tgz", tarGzip: true}, true
	case "darwin/arm64":
		return cloudflaredInstallTarget{assetName: "cloudflared-darwin-arm64.tgz", tarGzip: true}, true
	default:
		return cloudflaredInstallTarget{}, false
	}
}

func fetchCloudflaredReleaseAsset(ctx context.Context, client cloudflaredHTTPDoer, releaseURL, assetName string) (cloudflaredReleaseAsset, error) {
	parsedReleaseURL, err := url.Parse(strings.TrimSpace(releaseURL))
	if err != nil || parsedReleaseURL.String() != cloudflaredLatestReleaseURL || !allowedCloudflaredHTTPURL(parsedReleaseURL) {
		return cloudflaredReleaseAsset{}, errCloudflaredDownloadDenied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedReleaseURL.String(), nil)
	if err != nil {
		return cloudflaredReleaseAsset{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "Autoto-cloudflared-installer")
	response, err := client.Do(request)
	if err != nil {
		return cloudflaredReleaseAsset{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cloudflaredReleaseAsset{}, errors.New("cloudflared release metadata request failed")
	}
	data, err := readBounded(response.Body, cloudflaredMetadataMaxBytes)
	if err != nil {
		return cloudflaredReleaseAsset{}, err
	}
	var release cloudflaredRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return cloudflaredReleaseAsset{}, errors.New("cloudflared release metadata is invalid")
	}
	if release.Draft || release.Prerelease || !validCloudflaredReleaseTag(release.TagName) {
		return cloudflaredReleaseAsset{}, errors.New("cloudflared release metadata is not a stable release")
	}

	var selected *cloudflaredReleaseAsset
	for index := range release.Assets {
		asset := &release.Assets[index]
		if asset.Name != assetName {
			continue
		}
		if selected != nil {
			return cloudflaredReleaseAsset{}, errors.New("cloudflared release contains duplicate assets")
		}
		selected = asset
	}
	if selected == nil {
		return cloudflaredReleaseAsset{}, errors.New("cloudflared release asset is unavailable")
	}
	if selected.Size <= 0 || selected.Size > cloudflaredAssetMaxBytes {
		return cloudflaredReleaseAsset{}, errors.New("cloudflared release asset size is invalid")
	}
	if _, err := cloudflaredDigestHex(selected.Digest); err != nil {
		return cloudflaredReleaseAsset{}, err
	}
	if err := validateCloudflaredAssetURL(selected.BrowserDownloadURL, release.TagName, assetName); err != nil {
		return cloudflaredReleaseAsset{}, err
	}
	return *selected, nil
}

func downloadCloudflaredAsset(ctx context.Context, client cloudflaredHTTPDoer, asset cloudflaredReleaseAsset, destination *os.File) error {
	if destination == nil {
		return errors.New("cloudflared download destination is unavailable")
	}
	if asset.Size <= 0 || asset.Size > cloudflaredAssetMaxBytes {
		return errors.New("cloudflared release asset size is invalid")
	}
	expectedDigest, err := cloudflaredDigestHex(asset.Digest)
	if err != nil {
		return err
	}
	parsedURL, err := url.Parse(strings.TrimSpace(asset.BrowserDownloadURL))
	if err != nil || !allowedCloudflaredHTTPURL(parsedURL) || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return errCloudflaredDownloadDenied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "Autoto-cloudflared-installer")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("cloudflared release asset request failed")
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return errors.New("cloudflared release asset uses an unsupported content encoding")
	}
	if response.ContentLength > cloudflaredAssetMaxBytes || (response.ContentLength >= 0 && response.ContentLength != asset.Size) {
		return errors.New("cloudflared release asset length does not match metadata")
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := destination.Truncate(0); err != nil {
		return err
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hasher), io.LimitReader(response.Body, cloudflaredAssetMaxBytes+1))
	if err != nil {
		return err
	}
	if written != asset.Size || written > cloudflaredAssetMaxBytes {
		return errors.New("cloudflared release asset length does not match metadata")
	}
	actualDigest := hex.EncodeToString(hasher.Sum(nil))
	if actualDigest != expectedDigest {
		return errors.New("cloudflared release asset checksum does not match metadata")
	}
	return destination.Sync()
}

func validateCloudflaredAssetURL(rawURL, tag, assetName string) error {
	if !validCloudflaredReleaseTag(tag) || strings.TrimSpace(assetName) == "" || filepath.Base(assetName) != assetName {
		return errCloudflaredDownloadDenied
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !allowedCloudflaredHTTPURL(parsed) || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return errCloudflaredDownloadDenied
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errCloudflaredDownloadDenied
	}
	expectedPath := "/cloudflare/cloudflared/releases/download/" + tag + "/" + assetName
	if parsed.EscapedPath() != expectedPath {
		return errCloudflaredDownloadDenied
	}
	return nil
}

func allowedCloudflaredHTTPURL(target *url.URL) bool {
	if target == nil || !target.IsAbs() || target.Opaque != "" || target.User != nil || !strings.EqualFold(target.Scheme, "https") {
		return false
	}
	if port := strings.TrimSpace(target.Port()); port != "" && port != "443" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(target.Hostname())) {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func validCloudflaredReleaseTag(tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" || len(tag) > 64 {
		return false
	}
	for index, char := range tag {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			if index == 0 && (char == '.' || char == '_' || char == '-') {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func cloudflaredDigestHex(digest string) (string, error) {
	algorithm, value, ok := strings.Cut(strings.ToLower(strings.TrimSpace(digest)), ":")
	if !ok || algorithm != "sha256" || len(value) != sha256.Size*2 {
		return "", errors.New("cloudflared release asset checksum is invalid")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("cloudflared release asset checksum is invalid")
	}
	return value, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil || maximum <= 0 {
		return nil, errors.New("bounded reader is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("response exceeds the allowed size")
	}
	return data, nil
}

func prepareCloudflaredBinDir(homeDir string) (string, error) {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return "", errors.New("home directory is required")
	}
	binDir := filepath.Join(homeDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(binDir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("managed binary directory must be a real directory")
	}
	if err := os.Chmod(binDir, 0o700); err != nil {
		return "", err
	}
	return binDir, nil
}

func extractCloudflaredTarGzip(archivePath string, destination *os.File) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer compressed.Close()
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := destination.Truncate(0); err != nil {
		return err
	}

	reader := tar.NewReader(compressed)
	found := false
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		cleanName := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if cleanName == "." || header.FileInfo().IsDir() {
			continue
		}
		regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		if cleanName != temporaryTunnelBinary || !regular {
			return errors.New("cloudflared release archive contains an unexpected entry")
		}
		if found || header.Size <= 0 || header.Size > cloudflaredAssetMaxBytes {
			return errors.New("cloudflared release archive executable is invalid")
		}
		written, copyErr := io.CopyN(destination, reader, header.Size)
		if copyErr != nil || written != header.Size {
			return errors.New("cloudflared release archive executable is incomplete")
		}
		found = true
	}
	if !found {
		return errors.New("cloudflared release archive does not contain the executable")
	}
	return nil
}

func commitCloudflaredBinary(sourcePath, destinationPath, goos string) error {
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(destinationPath) == "" {
		return errors.New("cloudflared binary path is unavailable")
	}
	if err := os.Chmod(sourcePath, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(destinationPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("managed cloudflared destination must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Rename(sourcePath, destinationPath); err != nil {
		if removeErr := os.Remove(destinationPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(sourcePath, destinationPath); retryErr != nil {
			return retryErr
		}
	}
	if !validCloudflaredBinary(destinationPath, goos) {
		_ = os.Remove(destinationPath)
		return errors.New("installed cloudflared binary is invalid")
	}
	return nil
}

func validCloudflaredBinary(binaryPath, goos string) bool {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return false
	}
	info, err := os.Lstat(binaryPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(goos), "windows") || info.Mode().Perm()&0o100 != 0
}
