package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  OAuth 2.0 + PKCE for Codebase CLI
// ──────────────────────────────────────────────────────────────

const (
	oauthClientID    = "codebase-cli"
	oauthBaseURL     = "https://codebase.foundation/api"
	oauthAuthorizeURL = oauthBaseURL + "/oauth/authorize"
	oauthTokenURL    = oauthBaseURL + "/oauth/token"
	oauthRevokeURL   = oauthBaseURL + "/oauth/revoke"
	oauthUserInfoURL = oauthBaseURL + "/oauth/userinfo"
	oauthScopes      = "inference projects credits"
)

// Credentials stored at ~/.codebase/credentials.json
type OAuthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp
	Scopes       string `json:"scopes"`
	UserID       string `json:"user_id,omitempty"`
	Email        string `json:"email,omitempty"`
}

// ──────────────────────────────────────────────────────────────
//  PKCE Helpers
// ──────────────────────────────────────────────────────────────

func generateCodeVerifier() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func generateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func generateState() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ──────────────────────────────────────────────────────────────
//  Credential Storage (~/.codebase/credentials.json, mode 0600)
// ──────────────────────────────────────────────────────────────

func credentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codebase", "credentials.json")
}

func loadCredentials() (*OAuthCredentials, error) {
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		return nil, err
	}
	var creds OAuthCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func saveCredentials(creds *OAuthCredentials) error {
	dir := filepath.Dir(credentialsPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(credentialsPath(), data, 0600)
}

func deleteCredentials() error {
	return os.Remove(credentialsPath())
}

// ──────────────────────────────────────────────────────────────
//  Token Management
// ──────────────────────────────────────────────────────────────

// IsLoggedIn checks if valid credentials exist.
func IsLoggedIn() bool {
	creds, err := loadCredentials()
	if err != nil {
		return false
	}
	return creds.AccessToken != "" && creds.RefreshToken != ""
}

// GetAccessToken returns a valid access token, refreshing if expired.
func GetAccessToken() (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", fmt.Errorf("not logged in — run 'codebase login'")
	}

	// Check if access token is expired (with 5 minute buffer)
	if time.Now().Unix() > creds.ExpiresAt-300 {
		// Refresh the token
		newCreds, err := refreshToken(creds.RefreshToken)
		if err != nil {
			// Refresh failed — credentials are stale
			return "", fmt.Errorf("session expired — run 'codebase login' to re-authenticate: %w", err)
		}
		creds = newCreds
	}

	return creds.AccessToken, nil
}

func refreshToken(refreshTok string) (*OAuthCredentials, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     oauthClientID,
		"refresh_token": refreshTok,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", oauthTokenURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	creds := &OAuthCredentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		Scopes:       tokenResp.Scope,
	}

	if err := saveCredentials(creds); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}

	return creds, nil
}

// ──────────────────────────────────────────────────────────────
//  Login Flow (Browser OAuth + PKCE)
// ──────────────────────────────────────────────────────────────

// Login performs the full OAuth PKCE flow:
// 1. Start localhost callback server
// 2. Open browser to auth page
// 3. Wait for callback with auth code
// 4. Exchange code for tokens
// 5. Save credentials
func Login() error {
	// Generate PKCE pair
	codeVerifier := generateCodeVerifier()
	codeChallenge := generateCodeChallenge(codeVerifier)
	state := generateState()

	// Start localhost listener on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to start callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	// Channel to receive the auth code
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// HTTP handler for the callback
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		receivedState := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>%s</p><p>You can close this tab.</p></body></html>", errParam)
			errCh <- fmt.Errorf("auth error: %s", errParam)
			return
		}

		if receivedState != state {
			w.WriteHeader(400)
			fmt.Fprintf(w, "<html><body><h2>Invalid state</h2><p>CSRF protection failed. Please try again.</p></body></html>")
			errCh <- fmt.Errorf("state mismatch: expected %s, got %s", state, receivedState)
			return
		}

		if code == "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, "<html><body><h2>No code received</h2><p>Please try again.</p></body></html>")
			errCh <- fmt.Errorf("no authorization code received")
			return
		}

		// Success page
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="background:#000;color:#fff;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
			<div style="text-align:center">
				<h1 style="font-size:3rem;margin:0">&#10003;</h1>
				<h2>Logged in to Codebase</h2>
				<p style="color:#888">You can close this tab and return to the terminal.</p>
			</div>
		</body></html>`)

		codeCh <- code
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer server.Close()

	// Build auth URL with proper URL encoding
	authParams := url.Values{}
	authParams.Set("oauth", "true")
	authParams.Set("client_id", oauthClientID)
	authParams.Set("redirect_uri", redirectURI)
	authParams.Set("code_challenge", codeChallenge)
	authParams.Set("code_challenge_method", "S256")
	authParams.Set("scope", oauthScopes)
	authParams.Set("state", state)
	authURL := fmt.Sprintf("%s/login?%s",
		strings.TrimSuffix(oauthBaseURL, "/api"),
		authParams.Encode(),
	)

	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If the browser doesn't open, visit:\n  %s\n\n", authURL)

	// Open browser
	openBrowser(authURL)

	// Wait for auth code (60 second timeout)
	fmt.Println("Waiting for authentication...")
	select {
	case code := <-codeCh:
		// Exchange code for tokens
		return exchangeCodeForTokens(code, state, codeVerifier, redirectURI)
	case err := <-errCh:
		return err
	case <-time.After(120 * time.Second):
		return fmt.Errorf("authentication timed out — please try again")
	}
}

func exchangeCodeForTokens(code, state, codeVerifier, redirectURI string) error {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     oauthClientID,
		"code":          code,
		"code_verifier": codeVerifier,
		"redirect_uri":  redirectURI,
		"state":         state,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", oauthTokenURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to parse token response: %w", err)
	}

	// Fetch user info
	userInfo, _ := fetchUserInfo(tokenResp.AccessToken)

	creds := &OAuthCredentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		Scopes:       tokenResp.Scope,
	}
	if userInfo != nil {
		creds.UserID = userInfo.ID
		creds.Email = userInfo.Email
	}

	if err := saveCredentials(creds); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	name := creds.Email
	if name == "" {
		name = creds.UserID
	}
	fmt.Printf("\nAuthenticated as %s\n", name)
	fmt.Println("Credentials saved to ~/.codebase/credentials.json")
	return nil
}

func fetchUserInfo(accessToken string) (*struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", oauthUserInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("userinfo failed: %d", resp.StatusCode)
	}

	var info struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	return &info, nil
}

// Logout revokes the refresh token and deletes local credentials.
func Logout() error {
	creds, err := loadCredentials()
	if err != nil {
		return fmt.Errorf("not logged in")
	}

	// Revoke refresh token on server
	if creds.RefreshToken != "" {
		body, _ := json.Marshal(map[string]string{
			"token":     creds.RefreshToken,
			"client_id": oauthClientID,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req, _ := http.NewRequestWithContext(ctx, "POST", oauthRevokeURL, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		http.DefaultClient.Do(req) // Best effort — don't fail on revoke errors
	}

	if err := deleteCredentials(); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}

// ──────────────────────────────────────────────────────────────
//  Browser opener
// ──────────────────────────────────────────────────────────────

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start() // Fire and forget — don't block if browser fails
}
