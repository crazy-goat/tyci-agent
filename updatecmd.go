package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// updateAPIURL is the GitHub API base URL. Tests replace it with an
// httptest.Server URL.
var updateAPIURL = "https://api.github.com"

const updateRepoPath = "/repos/crazy-goat/tyci-agent/releases"

const updateChecksumsName = "checksums.txt"

// updateClient has a timeout, because a stalled download must not hang the
// command. The default client has none.
var updateClient = &http.Client{Timeout: 5 * time.Minute}

type updateOptions struct {
	check   bool
	version string // release tag to install; empty means the latest release
	force   bool
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and install the latest tyci release",
	Long: `Download the latest tyci release, check its SHA-256 against checksums.txt
and replace the running binary. The new file is written next to the old one.
Set GITHUB_TOKEN or GH_TOKEN to avoid the GitHub API rate limit.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		check, _ := cmd.Flags().GetBool("check")
		tag, _ := cmd.Flags().GetString("version")
		force, _ := cmd.Flags().GetBool("force")
		exe, err := runningBinary()
		if err != nil {
			return err
		}
		return runUpdate(cmd.Context(), cmd.OutOrStdout(), updateOptions{check: check, version: tag, force: force}, resolveVersion(), exe)
	},
}

func init() {
	updateCmd.Flags().Bool("check", false, "only print the latest version and whether an update exists")
	updateCmd.Flags().String("version", "", "install this release tag (vX.Y.Z) instead of the latest")
	updateCmd.Flags().Bool("force", false, "allow a dev build, and re-install the same version")
	rootCmd.AddCommand(updateCmd)
}

// runningBinary returns the path of the running binary with symlinks resolved.
func runningBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running binary: %w", err)
	}
	return filepath.EvalSymlinks(exe)
}

// runUpdate checks the release and, unless only checking, replaces exe with
// the release asset for this platform. The old file stays unchanged on any error.
func runUpdate(ctx context.Context, out io.Writer, opts updateOptions, current, exe string) error {
	if opts.check && opts.version != "" {
		return errors.New("use either --check or --version, not both")
	}
	rel, err := fetchRelease(ctx, opts.version)
	if err != nil {
		return err
	}
	latest := rel.TagName

	if opts.check {
		switch {
		case isDevBuild(current):
			fmt.Fprintf(out, "current: %s (dev build)\nlatest:  %s\n", current, latest)
		case compareVersions(latest, current) > 0:
			fmt.Fprintf(out, "current: %s\nlatest:  %s\nupdate available: run `tyci update`\n", current, latest)
		default:
			fmt.Fprintf(out, "current: %s\nlatest:  %s\nup to date\n", current, latest)
		}
		return nil
	}

	if isDevBuild(current) {
		if !opts.force {
			return fmt.Errorf("%s is a dev build, not a release; run with --force to replace it with %s", current, latest)
		}
	} else if !opts.force {
		c := compareVersions(latest, current)
		// Without --version, an older release never replaces a newer build.
		if c == 0 || (c < 0 && opts.version == "") {
			fmt.Fprintf(out, "already at %s\n", current)
			return nil
		}
	}

	name := "tyci_" + runtime.GOOS + "_" + runtime.GOARCH
	asset, ok := findAsset(rel, name)
	if !ok {
		return fmt.Errorf("release %s has no asset %s for this platform", latest, name)
	}
	sums, ok := findAsset(rel, updateChecksumsName)
	if !ok {
		return fmt.Errorf("release %s has no %s", latest, updateChecksumsName)
	}
	want, err := fetchExpectedSum(ctx, sums.URL, name)
	if err != nil {
		return err
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".tyci-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w\ninstall %s by hand: download %s from %s and copy it to %s",
			dir, err, latest, name, rel.HTMLURL, exe)
	}
	tmpName := tmp.Name()
	installed := false
	defer func() {
		if !installed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	if err := download(ctx, asset.URL, io.MultiWriter(tmp, h)); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s; the binary was not replaced", name, want, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	installed = true

	fmt.Fprintf(out, "updated %s -> %s\n%s\nrestart tyci to use it\n", current, latest, rel.HTMLURL)
	return nil
}

// fetchRelease returns the release with the given tag, or the latest release
// when tag is empty. GitHub's "latest" skips pre-releases and drafts.
func fetchRelease(ctx context.Context, tag string) (ghRelease, error) {
	path := updateRepoPath + "/latest"
	if tag != "" {
		if _, ok := parseSemver(tag); !ok {
			return ghRelease{}, fmt.Errorf("invalid version %q, want vX.Y.Z", tag)
		}
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		path = updateRepoPath + "/tags/" + tag
	}
	body, err := httpGet(ctx, updateAPIURL+path, true)
	if err != nil {
		return ghRelease{}, fmt.Errorf("fetch release: %w", err)
	}
	defer body.Close()
	var rel ghRelease
	if err := json.NewDecoder(body).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return ghRelease{}, errors.New("release has no tag name")
	}
	return rel, nil
}

// fetchExpectedSum returns the SHA-256 that checksums.txt lists for name. The
// file has the format of `sha256sum`: "<hex>  <name>" per line.
func fetchExpectedSum(ctx context.Context, url, name string) (string, error) {
	body, err := httpGet(ctx, url, false)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", updateChecksumsName, err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", updateChecksumsName, err)
	}
	return parseChecksum(string(data), name)
}

func parseChecksum(text, name string) (string, error) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != sha256.Size*2 {
			return "", fmt.Errorf("malformed checksum for %s in %s", name, updateChecksumsName)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", fmt.Errorf("malformed checksum for %s in %s", name, updateChecksumsName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("no checksum for %s in %s", name, updateChecksumsName)
}

func findAsset(rel ghRelease, name string) (ghAsset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// download streams url into w. It does not send the API token: asset URLs
// are not API calls.
func download(ctx context.Context, url string, w io.Writer) error {
	body, err := httpGet(ctx, url, false)
	if err != nil {
		return err
	}
	defer body.Close()
	_, err = io.Copy(w, body)
	return err
}

// httpGet returns the body of a 200 response. The API token is sent only when
// withToken is set, and never appears in an error message.
func httpGet(ctx context.Context, url string, withToken bool) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tyci-update")
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := updateToken(); withToken && tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := updateClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func updateToken() string {
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok
	}
	return os.Getenv("GH_TOKEN")
}

var (
	semverRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
	// Matches `git describe` output after a tag (vX.Y.Z-N-gHASH) and Go
	// pseudo-versions (vX.Y.Z-0.YYYYMMDDHHMMSS-HASH) of an untagged commit.
	devRe = regexp.MustCompile(`-\d+-g[0-9a-f]+|-0\.\d{14}-[0-9a-f]+`)
)

// isDevBuild reports whether v is not a release: "dev", a bare commit from
// `git describe --always`, a build after a tag, a pseudo-version or a dirty tree.
func isDevBuild(v string) bool {
	if _, ok := parseSemver(v); !ok {
		return true
	}
	return devRe.MatchString(v) || strings.Contains(v, "dirty")
}

type semver struct {
	nums [3]int
	pre  []string // pre-release identifiers; empty for a release
}

func parseSemver(v string) (semver, bool) {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return semver{}, false
	}
	var s semver
	for i := range s.nums {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		s.nums[i] = n
	}
	if m[4] != "" {
		s.pre = strings.Split(m[4], ".")
	}
	return s, true
}

// compareVersions returns -1, 0 or 1 when a is older, equal or newer than b.
// A value that is not a version compares as older than any release. Callers
// must check isDevBuild first when that matters.
func compareVersions(a, b string) int {
	sa, okA := parseSemver(a)
	sb, okB := parseSemver(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range sa.nums {
		if sa.nums[i] != sb.nums[i] {
			return cmpInt(sa.nums[i], sb.nums[i])
		}
	}
	// A release is newer than any pre-release of the same numbers.
	switch {
	case len(sa.pre) == 0 && len(sb.pre) == 0:
		return 0
	case len(sa.pre) == 0:
		return 1
	case len(sb.pre) == 0:
		return -1
	}
	return comparePreRelease(sa.pre, sb.pre)
}

func comparePreRelease(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdentifier(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a), len(b))
}

func compareIdentifier(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1 // numeric identifiers sort before alphanumeric ones
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
