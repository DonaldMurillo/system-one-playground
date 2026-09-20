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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

const (
	releasesAPI = "https://api.github.com/repos/DonaldMurillo/system-one-playground/releases?per_page=30"
	releaseBase = "https://github.com/DonaldMurillo/system-one-playground/releases/download"
)

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

func runUpdate(args []string) int {
	checkOnly := false
	if len(args) == 1 && args[0] == "--check" {
		checkOnly = true
	} else if len(args) != 0 {
		return fail(errors.New("usage: sysone update [--check]"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	version, tag, err := latestRelease(ctx)
	cancel()
	if err != nil {
		return fail(fmt.Errorf("check for updates: %w", err))
	}
	if compareVersion(version, sos.Version) <= 0 {
		fmt.Printf("SysOneScript %s is up to date.\n", sos.Version)
		return 0
	}
	if checkOnly {
		fmt.Printf("SysOneScript %s is available; you have %s. Run `sysone update`.\n", version, sos.Version)
		return 0
	}
	installCtx, installCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer installCancel()
	if err = installRelease(installCtx, version, tag); err != nil {
		return fail(fmt.Errorf("update: %w", err))
	}
	return 0
}

func latestRelease(ctx context.Context) (string, string, error) {
	url := os.Getenv("SYSONESCRIPT_RELEASES_API")
	if url == "" {
		url = releasesAPI
	}
	var releases []releaseInfo
	if err := getJSON(ctx, url, &releases); err != nil {
		return "", "", err
	}
	for _, release := range releases {
		if strings.HasPrefix(release.TagName, "vscode-v") {
			version := strings.TrimPrefix(release.TagName, "vscode-v")
			if version != "" {
				return version, release.TagName, nil
			}
		}
	}
	return "", "", errors.New("no SysOneScript CLI release found")
}

func getJSON(ctx context.Context, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sysone/"+sos.Version)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release service returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
}

func installRelease(ctx context.Context, version, tag string) error {
	base := os.Getenv("SYSONESCRIPT_RELEASE_BASE")
	if base == "" {
		base = releaseBase + "/" + tag
	}
	script := "install.sh"
	if runtime.GOOS == "windows" {
		script = "install.ps1"
	}
	tmp, err := os.MkdirTemp("", "sysonescript-update-*")
	if err != nil {
		return err
	}
	checksums, err := download(ctx, base+"/checksums.txt", 1<<20)
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	content, err := download(ctx, base+"/"+script, 1<<20)
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err = verifyReleaseFile(script, content, checksums); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	path := filepath.Join(tmp, script)
	if err = os.WriteFile(path, content, 0o700); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	installDir := filepath.Dir(executable)
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path, "-Version", version, "-InstallDir", installDir, "-WaitForProcessId", fmt.Sprint(os.Getpid()), "-CleanupScript", "-NoPathUpdate")
		if err = cmd.Start(); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		fmt.Printf("SysOneScript %s update scheduled. Close this terminal process; the installer will finish in the background.\n", version)
		return nil
	}
	defer os.RemoveAll(tmp)
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "SYSONESCRIPT_VERSION="+version, "SYSONESCRIPT_INSTALL_DIR="+installDir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return data, nil
}

func verifyReleaseFile(name string, content, checksums []byte) error {
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "./") == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	sum := sha256.Sum256(content)
	if want == "" || hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("checksum verification failed for %s", name)
	}
	return nil
}

func compareVersion(a, b string) int {
	parse := func(value string) [3]int {
		var result [3]int
		fmt.Sscanf(strings.TrimPrefix(value, "v"), "%d.%d.%d", &result[0], &result[1], &result[2])
		return result
	}
	left, right := parse(a), parse(b)
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}
