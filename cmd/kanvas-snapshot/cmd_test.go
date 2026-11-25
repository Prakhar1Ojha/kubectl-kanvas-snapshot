package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsValidEmail(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"test@example.com", true},
		{"user.name+tag@sub.domain.org", true},
		{"invalid-email", false},
		{"@missinguser.com", false},
		{"missingdomain@", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			got := IsValidEmail(tt.email)
			if got != tt.valid {
				t.Errorf("IsValidEmail(%q) = %v; want %v", tt.email, got, tt.valid)
			}
		})
	}
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://raw.githubusercontent.com/kubernetes/website/main/manifest.yaml", true},
		{"http://example.com/manifest.yml", true},
		{"/path/to/local/manifest.yaml", false},
		{"./manifest.yaml", false},
		{"manifest.yaml", false},
		{"-", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsURL(tt.input)
			if got != tt.want {
				t.Errorf("IsURL(%q) = %v; want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractNameFromURI(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"https://raw.githubusercontent.com/kubernetes/website/main/deployment.yaml", "deployment"},
		{"https://example.com/charts/my-app-v1.0.0.tgz", "my-app-v1.0.0"},
		{"https://example.com/", "manifest"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got := ExtractNameFromURI(tt.uri)
			if got != tt.want {
				t.Errorf("ExtractNameFromURI(%q) = %q; want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestExtractNameFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"./deployments/frontend.yaml", "frontend"},
		{"/var/k8s/service.yml", "service"},
		{"pod.json", "pod"},
		{".", "k8s-manifest"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ExtractNameFromPath(tt.path)
			if got != tt.want {
				t.Errorf("ExtractNameFromPath(%q) = %q; want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestAggregateManifests(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "deploy.yaml")
	file2 := filepath.Join(tmpDir, "svc.yaml")

	content1 := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: nginx-deployment"
	content2 := "apiVersion: v1\nkind: Service\nmetadata:\n  name: nginx-service"

	if err := os.WriteFile(file1, []byte(content1), 0644); err != nil {
		t.Fatalf("failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte(content2), 0644); err != nil {
		t.Fatalf("failed to write file2: %v", err)
	}

	// Test single file
	bytesOut, name, err := AggregateManifests([]string{file1})
	if err != nil {
		t.Fatalf("AggregateManifests single file failed: %v", err)
	}
	if name != "deploy.yaml" {
		t.Errorf("unexpected name: %s", name)
	}
	if !strings.Contains(string(bytesOut), "nginx-deployment") {
		t.Errorf("content missing nginx-deployment")
	}

	// Test multiple files
	combined, multiName, err := AggregateManifests([]string{file1, file2})
	if err != nil {
		t.Fatalf("AggregateManifests multiple files failed: %v", err)
	}
	if multiName != "manifest.yaml" {
		t.Errorf("unexpected multi-file aggregate name: got %q, want %q", multiName, "manifest.yaml")
	}
	combinedStr := string(combined)
	if !strings.Contains(combinedStr, "nginx-deployment") || !strings.Contains(combinedStr, "nginx-service") {
		t.Errorf("combined manifest missing resources: %s", combinedStr)
	}
	if !strings.Contains(combinedStr, "---") {
		t.Errorf("combined manifest missing document delimiter: %s", combinedStr)
	}

	// Test directory
	dirCombined, dirName, err := AggregateManifests([]string{tmpDir})
	if err != nil {
		t.Fatalf("AggregateManifests directory failed: %v", err)
	}
	if dirName != "manifest.yaml" {
		t.Errorf("unexpected directory aggregate name: got %q, want %q", dirName, "manifest.yaml")
	}
	dirCombinedStr := string(dirCombined)
	if !strings.Contains(dirCombinedStr, "nginx-deployment") || !strings.Contains(dirCombinedStr, "nginx-service") {
		t.Errorf("directory scan missing resources: %s", dirCombinedStr)
	}
}

func TestDownloadAssetWithRetry(t *testing.T) {
	tmpDir := t.TempDir()
	destDir := filepath.Join(tmpDir, "sub")
	destFile := filepath.Join(destDir, "output.png")

	// Pre-create destination with existing content to verify atomic overwrite/preservation
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create destDir: %v", err)
	}
	initialContent := []byte("original-unmodified-content")
	if err := os.WriteFile(destFile, initialContent, 0o644); err != nil {
		t.Fatalf("failed to write initial destFile: %v", err)
	}

	// 1. Verify that a failed download preserves existing destination content and cleans up any temp files
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	failErr := DownloadAssetWithRetry(failServer.URL, destFile, 2, 10*time.Millisecond)
	if failErr == nil {
		t.Fatal("expected DownloadAssetWithRetry to fail on 500 status, got nil")
	}

	persistedContent, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("failed to read destFile after failure: %v", err)
	}
	if string(persistedContent) != string(initialContent) {
		t.Errorf("destFile was modified on failure: got %q, want %q", string(persistedContent), string(initialContent))
	}

	// Verify no leftover .tmp files
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("failed to read destDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("found leftover temporary download file: %s", entry.Name())
		}
	}

	// 2. Verify that a successful download atomically overwrites destination with new content
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("new-downloaded-png-data"))
	}))
	defer successServer.Close()

	if err := DownloadAssetWithRetry(successServer.URL, destFile, 2, 10*time.Millisecond); err != nil {
		t.Fatalf("DownloadAssetWithRetry failed: %v", err)
	}

	updatedContent, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(updatedContent) != "new-downloaded-png-data" {
		t.Errorf("unexpected content: got %q, want %q", string(updatedContent), "new-downloaded-png-data")
	}

	// Verify no leftover .tmp files after successful download
	entries, err = os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("failed to read destDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("found leftover temporary download file: %s", entry.Name())
		}
	}
}

func TestCreateMesheryDesignMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pattern/import" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		response := []map[string]interface{}{
			{
				"id":   "test-design-uuid-12345",
				"name": "nginx-manifest",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	MesheryAPIBaseURL = server.URL
	ProviderToken = "test-token"

	// Test URL variant
	designID, err := CreateMesheryDesignFromURL("https://example.com/manifest.yaml", "nginx", "user@example.com")
	if err != nil {
		t.Fatalf("CreateMesheryDesignFromURL failed: %v", err)
	}
	if designID != "test-design-uuid-12345" {
		t.Errorf("unexpected designID: %s", designID)
	}

	// Test Bytes variant
	designID2, err := CreateMesheryDesignFromBytes([]byte("kind: Pod"), "manifest.yaml", "pod", "")
	if err != nil {
		t.Fatalf("CreateMesheryDesignFromBytes failed: %v", err)
	}
	if designID2 != "test-design-uuid-12345" {
		t.Errorf("unexpected designID: %s", designID2)
	}
}
