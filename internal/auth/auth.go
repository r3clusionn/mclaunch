// Package auth: Microsoft accounts (device code: Microsoft -> Xbox Live ->
// XSTS -> Minecraft services) and offline accounts, stored in accounts.json
// with mode 0600.
//
// Microsoft sign-in uses the official Minecraft launcher's client ID with
// Microsoft's consumer (login.live.com) endpoints by default, as many small
// launchers do: Minecraft services only accept app IDs Mojang approved. This
// is not sanctioned and Microsoft can stop it. An own Azure app ID (approved
// by Mojang) can be set instead and then uses the Azure AD endpoints.
package auth

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mclaunch/internal/paths"
)

type Account struct {
	ID       string `json:"id"` // Minecraft profile UUID (no dashes)
	Name     string `json:"name"`
	Type     string `json:"type"` // "msa" or "offline"
	Default  bool   `json:"default"`
	Skin     string `json:"skin,omitempty"`
	XUID     string `json:"xuid,omitempty"`
	Token    string `json:"token,omitempty"` // Minecraft access token
	Expires  int64  `json:"expires,omitempty"`
	Refresh  string `json:"refresh,omitempty"` // Microsoft refresh token
	ClientID string `json:"clientId,omitempty"`
}

var mu sync.Mutex

func file() string { return filepath.Join(paths.Root, "accounts.json") }

func List() []*Account {
	mu.Lock()
	defer mu.Unlock()
	return load()
}

func load() []*Account {
	b, err := os.ReadFile(file())
	if err != nil {
		return nil
	}
	var a []*Account
	json.Unmarshal(b, &a)
	return a
}

func save(a []*Account) error {
	os.MkdirAll(paths.Root, 0o755)
	b, _ := json.MarshalIndent(a, "", "  ")
	tmp := file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file())
}

// Upsert adds or replaces an account (by ID); the first account is the default.
func Upsert(acc *Account) error {
	mu.Lock()
	defer mu.Unlock()
	all := load()
	found := false
	for i, a := range all {
		if a.ID == acc.ID {
			acc.Default = a.Default
			all[i] = acc
			found = true
		}
	}
	if !found {
		acc.Default = len(all) == 0
		all = append(all, acc)
	}
	return save(all)
}

func Remove(id string) error {
	mu.Lock()
	defer mu.Unlock()
	var keep []*Account
	wasDefault := false
	for _, a := range load() {
		if a.ID == id {
			wasDefault = a.Default
			continue
		}
		keep = append(keep, a)
	}
	if wasDefault && len(keep) > 0 {
		keep[0].Default = true
	}
	return save(keep)
}

func SetDefault(id string) error {
	mu.Lock()
	defer mu.Unlock()
	all := load()
	for _, a := range all {
		a.Default = a.ID == id
	}
	return save(all)
}

// Get finds an account by ID or name; "" is the default account.
func Get(key string) (*Account, error) {
	for _, a := range List() {
		if (key == "" && a.Default) || a.ID == key || strings.EqualFold(a.Name, key) {
			return a, nil
		}
	}
	if key == "" {
		return nil, errors.New("no account: add one (offline accounts play singleplayer)")
	}
	return nil, fmt.Errorf("no account %q", key)
}

// Offline makes an offline account with the UUID the game itself uses for
// offline players (MD5 of "OfflinePlayer:<name>", version 3).
func Offline(name string) (*Account, error) {
	if len(name) < 3 || len(name) > 16 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
		return nil, errors.New("a name is 3 to 16 letters, digits or _")
	}
	h := md5.Sum([]byte("OfflinePlayer:" + name))
	h[6] = h[6]&0x0f | 0x30
	h[8] = h[8]&0x3f | 0x80
	return &Account{ID: fmt.Sprintf("%x", h), Name: name, Type: "offline"}, nil
}

// ---- Microsoft ---------------------------------------------------------------

var client = &http.Client{Timeout: 30 * time.Second}

func postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(req, out)
}

func postJSON(ctx context.Context, u string, body, out any, header ...string) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	return do(req, out)
}

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func do(req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return &httpError{resp.StatusCode, string(b)}
	}
	return json.Unmarshal(b, out)
}

// DeviceCode is what the user enters at the verification page.
type DeviceCode struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

const msTenant = "https://login.microsoftonline.com/consumers/oauth2/v2.0"

// OfficialClientID is the official Minecraft launcher's (Microsoft account,
// login.live.com) client ID.
const OfficialClientID = "00000000402b5328"

type endpoints struct{ device, token, scope string }

func endpointsFor(clientID string) endpoints {
	if clientID == OfficialClientID {
		return endpoints{"https://login.live.com/oauth20_connect.srf", "https://login.live.com/oauth20_token.srf",
			"service::user.auth.xboxlive.com::MBI_SSL"}
	}
	return endpoints{msTenant + "/devicecode", msTenant + "/token", "XboxLive.signin offline_access"}
}

// ClientID is the ID to sign in with: the configured one, else the official launcher's.
func ClientID(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return OfficialClientID
	}
	return strings.TrimSpace(configured)
}

func StartDeviceCode(ctx context.Context, clientID string) (*DeviceCode, error) {
	clientID = ClientID(clientID)
	ep := endpointsFor(clientID)
	form := url.Values{"client_id": {clientID}, "scope": {ep.scope}}
	if clientID == OfficialClientID {
		form.Set("response_type", "device_code")
	}
	var dc DeviceCode
	err := postForm(ctx, ep.device, form, &dc)
	return &dc, err
}

type msToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

// PollDeviceCode waits for the user to sign in, then signs in to Minecraft.
func PollDeviceCode(ctx context.Context, clientID string, dc *DeviceCode) (*Account, error) {
	clientID = ClientID(clientID)
	ep := endpointsFor(clientID)
	interval := time.Duration(max(dc.Interval, 5)) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var tok msToken
		err := postForm(ctx, ep.token, url.Values{
			"client_id": {clientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dc.DeviceCode},
		}, &tok)
		var he *httpError
		if errors.As(err, &he) && strings.Contains(he.Body, "authorization_pending") {
			continue
		}
		if errors.As(err, &he) && strings.Contains(he.Body, "slow_down") {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			return nil, err
		}
		return minecraftLogin(ctx, clientID, tok)
	}
	return nil, errors.New("the sign-in code expired")
}

// Refresh renews a Microsoft account's Minecraft token when it is (nearly) expired.
func Refresh(ctx context.Context, a *Account) (*Account, error) {
	if a.Type != "msa" || time.Now().Unix() < a.Expires-300 {
		return a, nil
	}
	ep := endpointsFor(a.ClientID)
	var tok msToken
	if err := postForm(ctx, ep.token, url.Values{
		"client_id": {a.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {a.Refresh},
		"scope": {ep.scope},
	}, &tok); err != nil {
		return nil, fmt.Errorf("refreshing %s: %w (sign in again)", a.Name, err)
	}
	na, err := minecraftLogin(ctx, a.ClientID, tok)
	if err != nil {
		return nil, err
	}
	return na, Upsert(na)
}

func minecraftLogin(ctx context.Context, clientID string, tok msToken) (*Account, error) {
	var xbl struct {
		Token         string `json:"Token"`
		DisplayClaims struct {
			Xui []struct {
				Uhs string `json:"uhs"`
			} `json:"xui"`
		} `json:"DisplayClaims"`
	}
	if err := postJSON(ctx, "https://user.auth.xboxlive.com/user/authenticate", map[string]any{
		"Properties":   map[string]any{"AuthMethod": "RPS", "SiteName": "user.auth.xboxlive.com", "RpsTicket": "d=" + tok.AccessToken},
		"RelyingParty": "http://auth.xboxlive.com", "TokenType": "JWT",
	}, &xbl); err != nil {
		return nil, fmt.Errorf("Xbox Live: %w", err)
	}
	var xsts struct {
		Token         string `json:"Token"`
		DisplayClaims struct {
			Xui []struct {
				Uhs string `json:"uhs"`
				Xid string `json:"xid"`
			} `json:"xui"`
		} `json:"DisplayClaims"`
	}
	if err := postJSON(ctx, "https://xsts.auth.xboxlive.com/xsts/authorize", map[string]any{
		"Properties":   map[string]any{"SandboxId": "RETAIL", "UserTokens": []string{xbl.Token}},
		"RelyingParty": "rp://api.minecraftservices.com/", "TokenType": "JWT",
	}, &xsts); err != nil {
		var he *httpError
		if errors.As(err, &he) && strings.Contains(he.Body, "2148916233") {
			return nil, errors.New("this Microsoft account has no Xbox profile yet: create one at xbox.com")
		}
		return nil, fmt.Errorf("Xbox security token: %w", err)
	}
	if len(xsts.DisplayClaims.Xui) == 0 {
		return nil, errors.New("Xbox returned no user")
	}
	var mc struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := postJSON(ctx, "https://api.minecraftservices.com/authentication/login_with_xbox", map[string]any{
		"identityToken": "XBL3.0 x=" + xsts.DisplayClaims.Xui[0].Uhs + ";" + xsts.Token,
	}, &mc); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == 403 {
			return nil, errors.New("Minecraft services refused this app: its Azure app ID is not approved by Mojang yet")
		}
		return nil, fmt.Errorf("Minecraft sign-in: %w", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.minecraftservices.com/minecraft/profile", nil)
	req.Header.Set("Authorization", "Bearer "+mc.AccessToken)
	var prof struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Skins []struct {
			URL   string `json:"url"`
			State string `json:"state"`
		} `json:"skins"`
	}
	if err := do(req, &prof); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == 404 {
			return nil, errors.New("this account doesn't own Minecraft: Java Edition")
		}
		return nil, fmt.Errorf("Minecraft profile: %w", err)
	}
	a := &Account{ID: prof.ID, Name: prof.Name, Type: "msa", Token: mc.AccessToken,
		Expires: time.Now().Unix() + mc.ExpiresIn, Refresh: tok.RefreshToken, ClientID: clientID,
		XUID: xsts.DisplayClaims.Xui[0].Xid}
	for _, s := range prof.Skins {
		if s.State == "ACTIVE" {
			a.Skin = s.URL
		}
	}
	return a, nil
}
