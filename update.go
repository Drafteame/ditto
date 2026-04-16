package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

const githubReleasesAPI = "https://api.github.com/repos/dtlucho/ditto/releases/latest"

// checkForUpdate queries GitHub Releases and returns the latest version tag
// and a download URL for the current platform.
func checkForUpdate() (latestVersion, downloadURL string, err error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(githubReleasesAPI)
	if err != nil {
		return "", "", fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("failed to parse release: %w", err)
	}

	// Find the download URL for the current platform
	downloadURL = release.HTMLURL // fallback to release page
	platformSuffix := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	for _, asset := range release.Assets {
		if contains(asset.Name, platformSuffix) {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	return release.TagName, downloadURL, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
