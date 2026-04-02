package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const pullEndpoint = oauthBaseURL + "/cli/projects"

// PullProject downloads a project from Codebase and extracts it to the current directory.
func PullProject(projectID string) error {
	token, err := GetAccessToken()
	if err != nil {
		return err
	}

	fmt.Printf("Pulling project %s...\n", projectID)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/%s/pull", pullEndpoint, projectID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication expired — run 'codebase login'")
	}
	if resp.StatusCode == 404 {
		return fmt.Errorf("project %s not found", projectID)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pull failed (%d): %s", resp.StatusCode, string(body))
	}

	// Read the ZIP into memory
	zipData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Extract
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("invalid ZIP: %w", err)
	}

	// Create output directory
	outDir := projectID
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	fileCount := 0
	for _, f := range reader.File {
		targetPath := filepath.Join(outDir, f.Name)

		// Security: prevent path traversal
		if !filepath.HasPrefix(targetPath, filepath.Clean(outDir)+string(os.PathSeparator)) && targetPath != filepath.Clean(outDir) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.Name, err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open %s: %w", f.Name, err)
		}

		outFile, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create %s: %w", f.Name, err)
		}

		if _, err := io.Copy(outFile, rc); err != nil {
			outFile.Close()
			rc.Close()
			return fmt.Errorf("failed to write %s: %w", f.Name, err)
		}

		outFile.Close()
		rc.Close()
		fileCount++
	}

	fmt.Printf("Pulled %d files to ./%s/\n", fileCount, outDir)
	return nil
}
