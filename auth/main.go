// Navy Lily — auth + payments server.
//
// Responsibilities (and ONLY these — the rest of the site is static):
//  1. Email+password login via self-hosted Supabase Auth (GoTrue) for the
//     credentials, with our own opaque, revocable sessions on top (the secret's
//     SHA-256 hash is stored in public.auth_sessions; the cookie holds id.secret).
//  2. Create AbacatePay PIX charges and receive their webhooks to
//     grant/revoke paid membership.
//  3. Gate the protected/ lessons: logged-in AND an active paying member.
//
// Zero external Go dependencies: it talks to Supabase over plain HTTP
// (GoTrue at /auth/v1, PostgREST at /rest/v1) and to AbacatePay over HTTPS.
// Configure via env (.env).
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type Config struct {
	Port             string
	BindAddr         string // interface to listen on (default 127.0.0.1, local-only; cloudflared reaches it there)
	SupabaseURL      string // e.g. http://localhost:8000 (Kong gateway)
	AnonKey          string // public anon key (used for auth endpoints)
	ServiceRoleKey   string // service_role key (used for DB writes/reads)
	AbacateURL       string // AbacatePay API base, e.g. https://api.abacatepay.com/v2
	AbacateKey       string // AbacatePay API key (Bearer) for creating PIX charges
	WebhookSecret    string // shared secret AbacatePay sends as ?webhookSecret=
	PriceCents       int    // charge amount in cents (197 BRL -> 19700)
	ProductID        string // AbacatePay product id (prod_...) for the hosted card subscription
	OneTimeProductID string // one-time product (prod_...) for coupon/discounted hosted checkouts
	SiteURL          string // public URL of the site, for magic-link redirect
	ProtectedDir     string // filesystem dir with paid lessons (gated, outside PublicDir)
	PublicDir        string // filesystem dir with free lessons (served openly at /)
	SessionCookie    string
	SessionTTLDays   int  // how long a login session lives before it must be re-created
	Secure           bool // set Secure flag on cookie (true behind HTTPS)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required env var %s (see auth/.env.example)", key)
	}
	return v
}

// sessionTTLDays reads SESSION_TTL_DAYS (default 30, the cookie + server-side
// session lifetime). A non-positive or unparseable value falls back to 30.
func sessionTTLDays() int {
	if n, err := strconv.Atoi(env("SESSION_TTL_DAYS", "30")); err == nil && n > 0 {
		return n
	}
	return 30
}

func loadConfig() Config {
	// .env.local wins over .env for local dev (loadDotEnv never overrides an
	// already-set key, so loading it first makes its values take precedence).
	loadDotEnv(".env.local")
	loadDotEnv(".env")
	price, err := strconv.Atoi(env("PRICE_CENTS", "19700"))
	if err != nil || price <= 0 {
		log.Fatalf("PRICE_CENTS must be a positive integer (cents); got %q", env("PRICE_CENTS", "19700"))
	}
	return Config{
		Port:             env("PORT", "8090"),
		BindAddr:         env("BIND_ADDR", "127.0.0.1"),
		SupabaseURL:      strings.TrimRight(mustEnv("SUPABASE_URL"), "/"),
		AnonKey:          mustEnv("SUPABASE_ANON_KEY"),
		ServiceRoleKey:   mustEnv("SUPABASE_SERVICE_ROLE_KEY"),
		AbacateURL:       strings.TrimRight(env("ABACATE_API_URL", "https://api.abacatepay.com/v2"), "/"),
		AbacateKey:       mustEnv("ABACATE_PAY_API_KEY"),
		WebhookSecret:    mustEnv("ABACATE_WEBHOOK_SECRET"),
		PriceCents:       price,
		ProductID:        env("ABACATE_PRODUCT_ID", ""),         // empty -> card button disabled
		OneTimeProductID: env("ABACATE_ONETIME_PRODUCT_ID", ""), // empty -> coupon flow disabled
		SiteURL:          strings.TrimRight(env("SITE_URL", "http://localhost:8090"), "/"),
		ProtectedDir:     env("PROTECTED_DIR", "../protected"),
		PublicDir:        env("PUBLIC_DIR", "../public"),
		SessionCookie:    "nl_session",
		SessionTTLDays:   sessionTTLDays(),
		Secure:           env("COOKIE_SECURE", "false") == "true",
	}
}

// loadDotEnv reads simple KEY=VALUE lines from a .env file if present.
// Lines starting with # are ignored. Does not override already-set env vars.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Strip an inline comment (" #...") from unquoted values so that
		// .env.example's documented "KEY=value   # note" style works. Quoted
		// values keep everything inside the quotes (a # may be part of them).
		if !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, `'`) {
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		v = strings.Trim(v, `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

var cfg Config

// ---------------------------------------------------------------------------
// Supabase helpers (GoTrue + PostgREST over HTTP)
// ---------------------------------------------------------------------------

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Magic-link rate limiting: at most one link per email per window, so the
// login form can't be used to spam someone's inbox (or burn SMTP quota).
var (
	linkMu       sync.Mutex
	lastLinkSent = map[string]time.Time{}
)

const magicLinkWindow = 60 * time.Second

// allowMagicLink records an attempt and reports whether it's allowed now.
func allowMagicLink(email string) bool {
	linkMu.Lock()
	defer linkMu.Unlock()
	now := time.Now()
	// Opportunistic cleanup so the map doesn't grow without bound.
	for k, t := range lastLinkSent {
		if now.Sub(t) > magicLinkWindow {
			delete(lastLinkSent, k)
		}
	}
	if t, ok := lastLinkSent[email]; ok && now.Sub(t) < magicLinkWindow {
		return false
	}
	lastLinkSent[email] = now
	return true
}

func supabaseDo(method, path string, body any, headers map[string]string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.SupabaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return httpClient.Do(req)
}

// sendMagicLink asks GoTrue to email a one-time login link to the address.
// create_user defaults to true, so first-time buyers get an account made.
// next, if set, is the path the user wanted before logging in; it rides along
// on the redirect so they land back where they started.
func sendMagicLink(email, next string) error {
	redirect := cfg.SiteURL + "/auth/callback"
	if next != "" {
		redirect += "?next=" + url.QueryEscape(next)
	}
	body := map[string]any{
		"email": email,
		"options": map[string]any{
			"email_redirect_to": redirect,
		},
	}
	resp, err := supabaseDo("POST", "/auth/v1/otp", body, map[string]string{
		"apikey":        cfg.AnonKey,
		"Authorization": "Bearer " + cfg.AnonKey,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gotrue otp %d: %s", resp.StatusCode, b)
	}
	return nil
}

// userFromToken validates a GoTrue access token and returns the user's id and
// verified email. Used by the link-based flows (email confirmation, the anon
// buyer's one-time login link, password reset) that land a token in the URL
// fragment and post it to us, so we can mint our own session for that user.
func userFromToken(token string) (userID, email string, err error) {
	resp, err := supabaseDo("GET", "/auth/v1/user", nil, map[string]string{
		"apikey":        cfg.AnonKey,
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("invalid session")
	}
	var u struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", "", err
	}
	if u.ID == "" || u.Email == "" {
		return "", "", fmt.Errorf("no user on token")
	}
	return u.ID, strings.ToLower(u.Email), nil
}

// signUp registers an email+password account with GoTrue. GoTrue hashes the
// password (bcrypt) and, when email confirmations are on, sends a confirmation
// link that lands on /auth/callback. next rides along so the user returns to the
// page they came from. The email itself is delivered by GoTrue's configured SMTP
// (Resend), the same path the post-purchase login link already uses.
func signUp(email, password, next string) error {
	redirect := cfg.SiteURL + "/auth/callback"
	if next != "" {
		redirect += "?next=" + url.QueryEscape(next)
	}
	body := map[string]any{
		"email":    email,
		"password": password,
		"options":  map[string]any{"email_redirect_to": redirect},
	}
	resp, err := supabaseDo("POST", "/auth/v1/signup", body, map[string]string{
		"apikey":        cfg.AnonKey,
		"Authorization": "Bearer " + cfg.AnonKey,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gotrue signup %d: %s", resp.StatusCode, b)
	}
	return nil
}

// passwordSignIn verifies an email+password against GoTrue. On success it
// returns the user's id and verified email. GoTrue rejects wrong passwords and
// (when confirmations are on) unconfirmed accounts with a non-200, which we
// surface as a generic invalid-credentials error.
func passwordSignIn(email, password string) (userID, verifiedEmail string, err error) {
	resp, err := supabaseDo("POST", "/auth/v1/token?grant_type=password",
		map[string]any{"email": email, "password": password}, map[string]string{
			"apikey":        cfg.AnonKey,
			"Authorization": "Bearer " + cfg.AnonKey,
		})
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("invalid credentials")
	}
	var out struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if out.User.ID == "" {
		return "", "", fmt.Errorf("no user on token response")
	}
	return out.User.ID, strings.ToLower(out.User.Email), nil
}

// sendPasswordReset asks GoTrue to email a password-reset link (lands on
// /auth/callback, which routes recovery links to /reset). Delivered by GoTrue's
// SMTP (Resend), like every other auth email.
func sendPasswordReset(email string) error {
	body := map[string]any{
		"email":   email,
		"options": map[string]any{"email_redirect_to": cfg.SiteURL + "/auth/callback"},
	}
	resp, err := supabaseDo("POST", "/auth/v1/recover", body, map[string]string{
		"apikey":        cfg.AnonKey,
		"Authorization": "Bearer " + cfg.AnonKey,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gotrue recover %d: %s", resp.StatusCode, b)
	}
	return nil
}

// updatePassword sets a new password for the user identified by a GoTrue access
// token (the recovery token from the reset email link).
func updatePassword(accessToken, newPassword string) error {
	resp, err := supabaseDo("PUT", "/auth/v1/user",
		map[string]any{"password": newPassword}, map[string]string{
			"apikey":        cfg.AnonKey,
			"Authorization": "Bearer " + accessToken,
		})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gotrue update password %d: %s", resp.StatusCode, b)
	}
	return nil
}

// isActiveMember checks the active_members view for this email.
func isActiveMember(email string) (bool, error) {
	q := "/rest/v1/active_members?select=email&email=eq." + url.QueryEscape(strings.ToLower(email))
	resp, err := supabaseDo("GET", q, nil, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("postgrest %d: %s", resp.StatusCode, b)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// memberChargeID returns the abacate_charge_id currently on file for email and
// whether a member row exists at all. Used to make sure an expiring/cancelled
// charge only downgrades access if it's the charge that granted it — an
// abandoned duplicate PIX (or a fresh renewal attempt) expiring must not revoke
// a membership granted by a different, paid charge.
func memberChargeID(email string) (chargeID string, exists bool) {
	q := "/rest/v1/members?select=abacate_charge_id&email=eq." +
		url.QueryEscape(strings.ToLower(email)) + "&limit=1"
	resp, err := supabaseDo("GET", q, nil, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var rows []struct {
		ChargeID *string `json:"abacate_charge_id"`
	}
	if json.NewDecoder(resp.Body).Decode(&rows) != nil || len(rows) == 0 {
		return "", false
	}
	if rows[0].ChargeID == nil {
		return "", true
	}
	return *rows[0].ChargeID, true
}

// upsertMember grants or updates paid access keyed by email.
func upsertMember(m map[string]any) error {
	resp, err := supabaseDo("POST", "/rest/v1/members", m, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
		"Prefer":        "resolution=merge-duplicates",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert member %d: %s", resp.StatusCode, b)
	}
	return nil
}

func logPaymentEvent(ev map[string]any) {
	resp, err := supabaseDo("POST", "/rest/v1/payment_events", ev, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		log.Printf("payment_events log error: %v", err)
		return
	}
	resp.Body.Close()
}

// eventAlreadyProcessed reports whether we've already logged an AbacatePay
// event with this exact (charge_id, event_type, charge_status) triple.
// AbacatePay retries webhooks, so this dedupes replays — and because the
// original event is logged before we act, a stale "PAID" replayed after a
// refund is also blocked.
func eventAlreadyProcessed(chargeID, eventType, chargeStatus string) bool {
	if chargeID == "" {
		return false // can't dedupe without a key; non-purchase noise anyway
	}
	q := "/rest/v1/payment_events?select=id" +
		"&charge_id=eq." + url.QueryEscape(chargeID) +
		"&event_type=eq." + url.QueryEscape(eventType) +
		"&charge_status=eq." + url.QueryEscape(chargeStatus) +
		"&limit=1"
	resp, err := supabaseDo("GET", q, nil, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		log.Printf("eventAlreadyProcessed: %v", err)
		return false // fail open: better to risk a re-grant than to drop a sale
	}
	defer resp.Body.Close()
	var rows []map[string]any
	if json.NewDecoder(resp.Body).Decode(&rows) != nil {
		return false
	}
	return len(rows) > 0
}

// ---------------------------------------------------------------------------
// AbacatePay (Checkout Transparente — PIX over HTTPS)
// ---------------------------------------------------------------------------

func abacateDo(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.AbacateURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AbacateKey)
	return httpClient.Do(req)
}

// pixCharge is the slice of an AbacatePay transparent PIX charge we render.
type pixCharge struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	BRCode       string `json:"brCode"`       // copy-paste PIX code
	BRCodeBase64 string `json:"brCodeBase64"` // data: image for the QR <img>
}

// createPixCharge opens a one-time PIX charge for the buyer. metadata carries
// the buyer's email so the webhook (and any manual reconciliation) can map the
// payment back to the account that should be granted access.
func createPixCharge(email string) (pixCharge, error) {
	reqBody := map[string]any{
		"method": "PIX",
		"data": map[string]any{
			"amount":      cfg.PriceCents,
			"description": "Navy Lily — acesso anual às aulas pagas",
			"expiresIn":   3600,  // 1h to pay
			"externalId":  email, // our account email — webhook grants by this, not the typed one
			// No customer object: v2 rejects a partial customer ("all fields
			// required if used") and we only have the buyer's email. The email
			// rides on externalId + metadata.email instead.
			"metadata": map[string]any{"email": email},
		},
	}
	resp, err := abacateDo("POST", "/transparents/create", reqBody)
	if err != nil {
		return pixCharge{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Data    pixCharge `json:"data"`
		Error   any       `json:"error"`
		Success bool      `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return pixCharge{}, err
	}
	if resp.StatusCode >= 300 || out.Data.ID == "" {
		return pixCharge{}, fmt.Errorf("abacate create %d: %v", resp.StatusCode, out.Error)
	}
	return out.Data, nil
}

// checkPixStatus polls a charge's current status (PENDING/PAID/EXPIRED/...).
func checkPixStatus(id string) (string, error) {
	resp, err := abacateDo("GET", "/transparents/check?id="+url.QueryEscape(id), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("abacate check %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Data.Status, nil
}

// createCardSubscription opens an AbacatePay recurring subscription (the only
// way to take a credit card — the transparent API is PIX/Boleto only) and
// returns the hosted-checkout URL to redirect the buyer to. The product must
// have an ANNUALLY cycle; AbacatePay then auto-charges the card every year and
// fires subscription.renewed, so access renews without the user lifting a
// finger. externalId carries the account email so the webhook grants the right
// account regardless of what the buyer types on AbacatePay's page.
func createCardSubscription(email string) (string, error) {
	reqBody := map[string]any{
		"items":         []map[string]any{{"id": cfg.ProductID, "quantity": 1}},
		"methods":       []string{"CARD"},
		"externalId":    email,
		"returnUrl":     cfg.SiteURL + "/comprar",
		"completionUrl": cfg.SiteURL + "/obrigado",
		"metadata":      map[string]any{"email": email},
	}
	resp, err := abacateDo("POST", "/subscriptions/create", reqBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 || out.Data.URL == "" {
		return "", fmt.Errorf("abacate subscription %d: %v", resp.StatusCode, out.Error)
	}
	return out.Data.URL, nil
}

// createCheckout opens a hosted AbacatePay checkout for a one-time purchase,
// optionally applying a coupon. AbacatePay validates the coupon, computes the
// discount and tracks redemptions — so we never do discount math ourselves. The
// resulting payment fires the same webhook (externalId = account email) that
// grants access. Used only for the coupon flow; the no-coupon paths stay on the
// inline PIX charge and the card subscription.
func createCheckout(email, coupon string) (string, error) {
	body := map[string]any{
		"items":         []map[string]any{{"id": cfg.OneTimeProductID, "quantity": 1}},
		"methods":       []string{"PIX", "CARD"},
		"externalId":    email, // webhook grants by this
		"returnUrl":     cfg.SiteURL + "/comprar",
		"completionUrl": cfg.SiteURL + "/protected/",
		"metadata":      map[string]any{"email": email},
	}
	if coupon != "" {
		body["coupons"] = []string{coupon}
	}
	resp, err := abacateDo("POST", "/checkouts/create", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 || out.Data.URL == "" {
		return "", fmt.Errorf("abacate checkout %d: %v", resp.StatusCode, out.Error)
	}
	return out.Data.URL, nil
}

// couponValid reports whether a coupon code exists, is ACTIVE and still has
// redemptions left. We don't compute the discount — AbacatePay applies it on the
// hosted checkout — so this is only for fast feedback before redirecting.
func couponValid(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	resp, err := abacateDo("GET", "/coupons/get?id="+url.QueryEscape(code), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var out struct {
		Data struct {
			Status       string `json:"status"`
			MaxRedeems   int    `json:"maxRedeems"`
			RedeemsCount int    `json:"redeemsCount"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	d := out.Data
	if strings.ToUpper(d.Status) != "ACTIVE" {
		return false
	}
	if d.MaxRedeems >= 0 && d.RedeemsCount >= d.MaxRedeems {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Sessions — opaque, server-side, revocable (pilcrowonpaper model).
//
// A session token is "<id>.<base64url(secret)>". The id is the public handle;
// the secret is 32 random bytes. We persist only the SHA-256 hash of the secret
// (hex) in public.auth_sessions and compare it in constant time, so a leaked
// database can't be turned back into a valid cookie. The cookie never carries a
// GoTrue bearer token. Logout deletes the row, instantly revoking the session —
// which the old token-in-cookie scheme could not do.
// ---------------------------------------------------------------------------

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// hashSecret is the hex SHA-256 stored in auth_sessions.secret_hash.
func hashSecret(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

func makeSessionToken(id string, secret []byte) string {
	return id + "." + base64.RawURLEncoding.EncodeToString(secret)
}

// parseSessionToken splits "<id>.<secret>" back into its parts. ok is false for
// anything malformed (including the old base64-JSON cookies, which carry no dot).
func parseSessionToken(token string) (id string, secret []byte, ok bool) {
	i := strings.IndexByte(token, '.')
	if i <= 0 || i >= len(token)-1 {
		return "", nil, false
	}
	sec, err := base64.RawURLEncoding.DecodeString(token[i+1:])
	if err != nil {
		return "", nil, false
	}
	return token[:i], sec, true
}

// createServerSession mints a session for an already-verified user and persists
// only the secret's hash. Returns the full token to put in the cookie.
func createServerSession(userID, email string) (string, error) {
	id := base64.RawURLEncoding.EncodeToString(randBytes(20))
	secret := randBytes(32)
	row := map[string]any{
		"id":          id,
		"user_id":     userID,
		"email":       strings.ToLower(email),
		"secret_hash": hashSecret(secret),
	}
	resp, err := supabaseDo("POST", "/rest/v1/auth_sessions", row, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session %d: %s", resp.StatusCode, b)
	}
	return makeSessionToken(id, secret), nil
}

// validateServerSession resolves a cookie token to its email, or "" if the token
// is missing/forged/expired. The secret is compared in constant time; a session
// past the TTL is treated as invalid and best-effort deleted.
func validateServerSession(token string) string {
	id, secret, ok := parseSessionToken(token)
	if !ok {
		return ""
	}
	q := "/rest/v1/auth_sessions?select=email,secret_hash,created_at&id=eq." +
		url.QueryEscape(id) + "&limit=1"
	resp, err := supabaseDo("GET", q, nil, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var rows []struct {
		Email      string    `json:"email"`
		SecretHash string    `json:"secret_hash"`
		CreatedAt  time.Time `json:"created_at"`
	}
	if json.NewDecoder(resp.Body).Decode(&rows) != nil || len(rows) == 0 {
		return ""
	}
	r0 := rows[0]
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(r0.SecretHash)) != 1 {
		return ""
	}
	if time.Since(r0.CreatedAt) > time.Duration(cfg.SessionTTLDays)*24*time.Hour {
		go deleteServerSession(id)
		return ""
	}
	return strings.ToLower(r0.Email)
}

// deleteServerSession revokes a session by deleting its row. Best-effort.
func deleteServerSession(id string) {
	if id == "" {
		return
	}
	resp, err := supabaseDo("DELETE", "/rest/v1/auth_sessions?id=eq."+url.QueryEscape(id),
		nil, map[string]string{
			"apikey":        cfg.ServiceRoleKey,
			"Authorization": "Bearer " + cfg.ServiceRoleKey,
		})
	if err != nil {
		return
	}
	resp.Body.Close()
}

func setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cfg.SessionTTLDays * 24 * 60 * 60,
	})
}

// clearSession revokes the session server-side (so the token is dead even if the
// cookie lingers) and blanks the cookie.
func clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cfg.SessionCookie); err == nil {
		if id, _, ok := parseSessionToken(c.Value); ok {
			deleteServerSession(id)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: cfg.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
}

// currentEmail returns the verified email for the request, or "" if none. It is
// a single auth_sessions lookup — no per-request round-trip to GoTrue.
func currentEmail(r *http.Request) string {
	c, err := r.Cookie(cfg.SessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return validateServerSession(c.Value)
}

// ---------------------------------------------------------------------------
// Inline (anonymous) checkout: the Join widget on the lessons lets a visitor
// buy without logging in first. They type their e-mail in the widget; we bind
// the PIX charge to it so the status poll can grant access and mail a login
// link the moment the payment clears (the webhook stays the authoritative
// grant). The binding is in-memory only — losing it on restart just means the
// poll-grant optimization is skipped for that charge; the webhook still grants.
// ---------------------------------------------------------------------------

var (
	chargeEmailMu sync.Mutex
	chargeEmail   = map[string]string{} // chargeID -> buyer e-mail
	magicSent     = map[string]bool{}   // chargeID -> login link already mailed
)

func bindChargeEmail(chargeID, email string) {
	chargeEmailMu.Lock()
	chargeEmail[chargeID] = email
	chargeEmailMu.Unlock()
}

func lookupChargeEmail(chargeID string) string {
	chargeEmailMu.Lock()
	defer chargeEmailMu.Unlock()
	return chargeEmail[chargeID]
}

// persistChargeEmail records the chargeID->email mapping durably (alongside the
// in-memory map). The PIX webhook (data.pixQrCode) carries no email, so this
// row in payment_events is the only thing that can resolve a PIX charge back to
// its buyer after a restart — without it, a restart between charge creation and
// webhook delivery would lose the sale silently.
func persistChargeEmail(chargeID, email string) {
	logPaymentEvent(map[string]any{
		"charge_id": chargeID, "event_type": "charge.created",
		"charge_status": "", "email": strings.ToLower(strings.TrimSpace(email)),
		"payload": map[string]any{"email": email},
	})
}

// lookupChargeEmailDB resolves a chargeID to its buyer email from payment_events
// when the in-memory binding is gone (e.g. after a restart).
func lookupChargeEmailDB(chargeID string) string {
	if chargeID == "" {
		return ""
	}
	q := "/rest/v1/payment_events?select=email&charge_id=eq." + url.QueryEscape(chargeID) +
		"&email=not.is.null&order=received_at.desc&limit=1"
	resp, err := supabaseDo("GET", q, nil, map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var rows []struct {
		Email string `json:"email"`
	}
	if json.NewDecoder(resp.Body).Decode(&rows) != nil || len(rows) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(rows[0].Email))
}

// buyerEmail resolves the e-mail to charge: the logged-in account if there is
// one, otherwise the validated address the visitor typed into the Join widget.
func buyerEmail(r *http.Request) string {
	if e := currentEmail(r); e != "" {
		return e
	}
	e := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if strings.Contains(e, "@") {
		return e
	}
	return ""
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// handleLessons serves the free-lessons index, building the list from the
// files in PublicDir so a new lesson appears just by dropping it in. The
// shell (styling, search) lives in lessons.html; we inject <li>s at the
// <!--LESSONS--> marker. Lessons with an empty <title> (placeholders) are
// skipped. Glob returns paths sorted, so 001..009 stay in order.
func handleLessons(w http.ResponseWriter, r *http.Request) {
	shell, err := os.ReadFile(filepath.Join(cfg.PublicDir, "lessons.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	shell = bytes.Replace(shell, []byte("<!--LESSONS-->"), []byte(lessonList(cfg.PublicDir, "/")), 1)
	shell = bytes.Replace(shell, []byte("<!--PAID-->"), []byte(lessonList(cfg.ProtectedDir, "/protected/")), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(shell)
}

// lessonList renders the <li> links for every 0NN.html in dir that has a
// real <title>, linking under hrefPrefix. Placeholders (empty title) skipped.
func lessonList(dir, hrefPrefix string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9].html"))
	var list strings.Builder
	for _, f := range files {
		src, _ := os.ReadFile(f)
		title := between(string(src), "<title>", " — Navy Lily")
		if title == "" {
			continue
		}
		fmt.Fprintf(&list, "<li><a href=\"%s%s\">%s</a></li>\n", hrefPrefix, filepath.Base(f), title)
	}
	return list.String()
}

// between returns the text of s between the first a and the following b,
// trimmed; "" if either marker is missing.
func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[:j])
}

// handleSitemap builds sitemap.xml at request time from every *.html in
// PublicDir, so a new lesson is included just by dropping the file in (same
// "glob the dir" approach as handleLessons). The homepage ("/") is listed
// explicitly; each page's <lastmod> is its file mtime. Only PublicDir is
// walked — paid lessons under protected/ are gated and must stay out.
func handleSitemap(w http.ResponseWriter, r *http.Request) {
	files, _ := filepath.Glob(filepath.Join(cfg.PublicDir, "*.html"))
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	fmt.Fprintf(&b, "  <url><loc>%s/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>\n", cfg.SiteURL)
	for _, f := range files {
		lastmod := ""
		if info, err := os.Stat(f); err == nil {
			lastmod = fmt.Sprintf("<lastmod>%s</lastmod>", info.ModTime().UTC().Format("2006-01-02"))
		}
		fmt.Fprintf(&b, "  <url><loc>%s/%s</loc>%s<changefreq>weekly</changefreq><priority>0.8</priority></url>\n",
			cfg.SiteURL, filepath.Base(f), lastmod)
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(b.String()))
}

func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/login.html")
}

// handleProfilePage serves the account page. The page gates itself client-side
// off /me (the session cookie never reaches static files), bouncing logged-out
// visitors to /login?next=/profile — so the "Profile" header link no longer
// dumps already-signed-in members on the login form.
func handleProfilePage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/profile.html")
}

// handleStatic serves the free, open lessons from PublicDir. It is the
// catch-all ("/") route, so it must never reach the paid lessons: the served
// tree is rooted at PublicDir, and protected/ lives outside it. The only path
// to a paid lesson is /protected/, which handleProtected gates.
func handleStatic(w http.ResponseWriter, r *http.Request) {
	// filepath.Clean on a rooted path collapses any ../ so requests can't
	// escape PublicDir.
	clean := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "/" {
		// No landing page yet — send visitors to the first free lesson.
		http.Redirect(w, r, "/001.html", http.StatusSeeOther)
		return
	}
	full := filepath.Join(cfg.PublicDir, clean)
	if info, err := os.Stat(full); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

func handleSignupPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/signup.html")
}

func handleForgotPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/forgot.html")
}

func handleResetPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/reset.html")
}

// sanitizeNext keeps only same-origin relative paths as a post-login destination.
func sanitizeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	return next
}

// nextQS renders a sanitized next path as a &next=... query fragment (or "").
func nextQS(next string) string {
	if next == "" {
		return ""
	}
	return "&next=" + url.QueryEscape(next)
}

// handleSignin verifies an email+password against GoTrue and, on success, mints
// our own opaque session. On failure it bounces back to /login with ?erro=1 so
// the form can show a generic "wrong e-mail or password" message.
func handleSignin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	next := sanitizeNext(r.FormValue("next"))
	if !strings.Contains(email, "@") || password == "" {
		http.Redirect(w, r, "/login?erro=1"+nextQS(next), http.StatusSeeOther)
		return
	}
	userID, vEmail, err := passwordSignIn(email, password)
	if err != nil {
		http.Redirect(w, r, "/login?erro=1"+nextQS(next), http.StatusSeeOther)
		return
	}
	token, err := createServerSession(userID, vEmail)
	if err != nil {
		log.Printf("createServerSession (signin): %v", err)
		http.Error(w, "não foi possível entrar agora", http.StatusBadGateway)
		return
	}
	setSession(w, token)
	dest := next
	if dest == "" {
		dest = "/protected/"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handleSignup registers an email+password account with GoTrue, which emails a
// confirmation link. The user lands logged in after clicking it (handleSession).
func handleSignup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	next := sanitizeNext(r.FormValue("next"))
	if !strings.Contains(email, "@") {
		http.Redirect(w, r, "/signup?erro=email"+nextQS(next), http.StatusSeeOther)
		return
	}
	if len(password) < 8 || len(password) > 72 || password != confirm {
		// bcrypt (GoTrue) hard-caps passwords at 72 bytes; rejecting here keeps
		// the error accurate instead of falling through to the generic "e-mail em
		// uso" message when GoTrue 400s on an over-long password.
		http.Redirect(w, r, "/signup?erro=senha"+nextQS(next), http.StatusSeeOther)
		return
	}
	if err := signUp(email, password, next); err != nil {
		// GoTrue rejects e.g. weak/breached passwords or an already-registered
		// e-mail. Keep the reason generic so the form can't enumerate accounts.
		log.Printf("signUp: %v", err)
		http.Redirect(w, r, "/signup?erro=falha"+nextQS(next), http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "web/check-email.html")
}

// handleRecover sends a password-reset e-mail. It always reports success (and
// rate-limits per address) so the form can't be used to discover which e-mails
// have accounts.
func handleRecover(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if strings.Contains(email, "@") && allowMagicLink(email) {
		if err := sendPasswordReset(email); err != nil {
			log.Printf("sendPasswordReset: %v", err)
		}
	}
	http.ServeFile(w, r, "web/check-email.html")
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	// Link-based flows (email confirmation, the anon buyer's one-time login link,
	// password recovery) land here with a GoTrue token in the URL fragment (#...),
	// which the browser does NOT send to the server. callback.html reads the
	// fragment: recovery links are routed to /reset; everything else POSTs the
	// access_token to /auth/session so we can mint a session.
	http.ServeFile(w, r, "web/callback.html")
}

// handleSession bridges a GoTrue access token (from a confirmation / one-time
// login link) into our own opaque session: validate the token, then mint a
// server-side session for that user. The GoTrue token itself is never stored.
func handleSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AccessToken == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	userID, email, err := userFromToken(body.AccessToken)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	token, err := createServerSession(userID, email)
	if err != nil {
		log.Printf("createServerSession (session): %v", err)
		http.Error(w, "session error", http.StatusBadGateway)
		return
	}
	setSession(w, token)
	w.WriteHeader(http.StatusNoContent)
}

// handleResetPassword finishes a reset: reset.html holds the recovery access
// token from the email link's fragment and posts it here with the new password.
// We set the password via GoTrue, then mint a fresh session so the user lands
// logged in.
func handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken string `json:"access_token"`
		Password    string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.AccessToken == "" || len(body.Password) < 8 {
		http.Error(w, "dados inválidos", http.StatusBadRequest)
		return
	}
	userID, email, err := userFromToken(body.AccessToken)
	if err != nil {
		http.Error(w, "link inválido ou expirado", http.StatusUnauthorized)
		return
	}
	if err := updatePassword(body.AccessToken, body.Password); err != nil {
		log.Printf("updatePassword: %v", err)
		http.Error(w, "não foi possível alterar a senha", http.StatusBadGateway)
		return
	}
	token, err := createServerSession(userID, email)
	if err != nil {
		log.Printf("createServerSession (reset): %v", err)
		http.Error(w, "session error", http.StatusBadGateway)
		return
	}
	setSession(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleMe powers the UI: who am I, am I a paying member?
func handleMe(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	out := map[string]any{"logged_in": email != "", "email": email, "member": false}
	if email != "" {
		if ok, err := isActiveMember(email); err == nil {
			out["member"] = ok
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleProtected gates the paid lessons.
func handleProtected(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		// Not logged in -> straight to checkout.
		http.Redirect(w, r, "/comprar", http.StatusSeeOther)
		return
	}
	member, err := isActiveMember(email)
	if err != nil {
		log.Printf("isActiveMember: %v", err)
		http.Error(w, "erro ao verificar acesso", http.StatusBadGateway)
		return
	}
	if !member {
		// Logged in but hasn't bought (or expired) -> send to checkout.
		http.Redirect(w, r, "/comprar", http.StatusSeeOther)
		return
	}
	// Serve the requested file from the protected dir, safely.
	rel := strings.TrimPrefix(r.URL.Path, "/protected/")
	if rel == "" {
		rel = "index.html"
	}
	clean := filepath.Clean("/" + rel) // prevents ../ traversal
	full := filepath.Join(cfg.ProtectedDir, clean)
	http.ServeFile(w, r, full)
}

// handleBuy serves the PIX checkout page. Buying requires being logged in
// (access is granted by email), so anonymous visitors go to /login first and
// come back here. The page itself fetches /pix/new and polls /pix/status.
// handleJoinJS serves the inline Join/checkout widget injected at the end of
// every free lesson. It's a single static script; an explicit route keeps it
// out of the PublicDir static tree.
func handleJoinJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	http.ServeFile(w, r, "web/join.js")
}

func handleBuy(w http.ResponseWriter, r *http.Request) {
	if currentEmail(r) == "" {
		http.Redirect(w, r, "/login?next=/comprar", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "web/comprar.html")
}

// handlePixNew opens a fresh PIX charge for the logged-in buyer and returns the
// QR (brCodeBase64) + copy-paste code (brCode) as JSON for the checkout page.
func handlePixNew(w http.ResponseWriter, r *http.Request) {
	email := buyerEmail(r)
	if email == "" {
		http.Error(w, "e-mail inválido", http.StatusBadRequest)
		return
	}
	charge, err := createPixCharge(email)
	if err != nil {
		log.Printf("createPixCharge: %v", err)
		http.Error(w, "não foi possível gerar o PIX agora", http.StatusBadGateway)
		return
	}
	bindChargeEmail(charge.ID, email)
	persistChargeEmail(charge.ID, email) // durable: PIX webhook carries no email
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":           charge.ID,
		"brCode":       charge.BRCode,
		"brCodeBase64": charge.BRCodeBase64,
	})
}

// handleCardNew opens AbacatePay's hosted card checkout and redirects the
// browser to it. The buyer's e-mail comes from the session or the ?email= the
// Join widget passes for anonymous buyers. Access is granted later by the
// checkout.completed webhook (handled alongside transparent.completed).
// handleCouponCheck validates a coupon code for the checkout page (fast
// feedback before the buyer commits). Returns {"valid": bool}.
func handleCouponCheck(w http.ResponseWriter, r *http.Request) {
	valid := couponValid(r.URL.Query().Get("code"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"valid": valid})
}

// handleCheckoutNew opens a hosted checkout (PIX + card) for a coupon order and
// redirects the buyer to it. AbacatePay applies the discount and tracks the
// redemption; the payment webhook then grants access by externalId (the email).
func handleCheckoutNew(w http.ResponseWriter, r *http.Request) {
	email := buyerEmail(r)
	if email == "" {
		http.Redirect(w, r, "/login?next=/comprar", http.StatusSeeOther)
		return
	}
	if cfg.OneTimeProductID == "" {
		http.Error(w, "checkout indisponível", http.StatusServiceUnavailable)
		return
	}
	coupon := strings.TrimSpace(r.URL.Query().Get("coupon"))
	if coupon != "" && !couponValid(coupon) {
		http.Redirect(w, r, "/comprar?cupom=invalido", http.StatusSeeOther)
		return
	}
	url, err := createCheckout(email, coupon)
	if err != nil {
		log.Printf("createCheckout: %v", err)
		http.Error(w, "não foi possível abrir o checkout agora", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func handleCardNew(w http.ResponseWriter, r *http.Request) {
	email := buyerEmail(r)
	if email == "" {
		http.Redirect(w, r, "/login?next=/comprar", http.StatusSeeOther)
		return
	}
	if cfg.ProductID == "" {
		http.Error(w, "pagamento com cartão indisponível", http.StatusServiceUnavailable)
		return
	}
	url, err := createCardSubscription(email)
	if err != nil {
		log.Printf("createCardSubscription: %v", err)
		http.Error(w, "não foi possível abrir o checkout de cartão agora", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// handlePixStatus polls a charge for the logged-in buyer. When AbacatePay
// reports PAID, we grant access immediately (idempotent upsert) so the user
// doesn't have to wait for the webhook round-trip — the webhook stays as the
// authoritative, out-of-band confirmation and the source of refunds/cancels.
func handlePixStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	// Logged-in buyer: trust the session. Anonymous buyer (Join widget): trust
	// the e-mail we bound to this charge when it was created — never one supplied
	// by the poller — so nobody can claim someone else's paid charge.
	loggedIn := currentEmail(r)
	email := loggedIn
	if email == "" {
		email = lookupChargeEmail(id)
	}
	status, err := checkPixStatus(id)
	if err != nil {
		log.Printf("checkPixStatus: %v", err)
		http.Error(w, "erro ao consultar o pagamento", http.StatusBadGateway)
		return
	}
	mailed := false
	if (status == "PAID" || status == "APPROVED") && email != "" {
		if err := grantAccess(email, email, id); err != nil {
			log.Printf("grantAccess (poll): %v", err)
		}
		// Anonymous buyer: mail a magic link (once) so they can open the paid
		// lessons. Best-effort — access is already granted by e-mail regardless.
		if loggedIn == "" {
			chargeEmailMu.Lock()
			if !magicSent[id] {
				magicSent[id] = true
				mailed = true
			}
			chargeEmailMu.Unlock()
			if mailed {
				if err := sendMagicLink(email, "/protected/"); err != nil {
					log.Printf("sendMagicLink (post-pix): %v", err)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    status,
		"logged_in": loggedIn != "",
		"mailed":    mailed,
	})
}

// grantAccess gives (or renews) one year of paid access keyed by email.
// started_at is intentionally omitted: on first purchase the column default
// (now()) sets it; on renewal merge-duplicates would otherwise overwrite the
// original purchase date, so we leave it untouched.
func grantAccess(email, name, chargeID string) error {
	return upsertMember(map[string]any{
		"email":             strings.ToLower(email),
		"name":              name,
		"status":            "active",
		"abacate_charge_id": chargeID,
		"source":            "abacatepay",
		"expires_at":        time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339),
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------------------
// AbacatePay webhook
// ---------------------------------------------------------------------------

// verifyWebhookSecret constant-time compares the ?webhookSecret= AbacatePay
// appends to the webhook URL against the secret we configured.
func verifyWebhookSecret(got string) bool {
	return hmac.Equal([]byte(strings.TrimSpace(got)), []byte(cfg.WebhookSecret))
}

// verifyWebhookSignature is the AbacatePay skill's recommended defense-in-depth
// on top of the query secret: if the request carries X-Webhook-Timestamp /
// X-Webhook-Signature, reject stale replays (>5min) and bodies whose
// HMAC-SHA256(raw, secret) doesn't match. Headers are validated only when
// present, so this never rejects a valid webhook that omits them. Returns an
// empty string when the request is acceptable, or a reason to reject.
func verifyWebhookSignature(r *http.Request, raw []byte) string {
	if ts := r.Header.Get("X-Webhook-Timestamp"); ts != "" {
		n, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Now().Unix()-n > 300 {
			return "stale or invalid timestamp"
		}
	}
	if sig := r.Header.Get("X-Webhook-Signature"); sig != "" {
		mac := hmac.New(sha256.New, []byte(cfg.WebhookSecret))
		mac.Write(raw)
		want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(want)) {
			return "signature mismatch"
		}
	}
	return ""
}

func handleAbacateWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if !verifyWebhookSecret(r.URL.Query().Get("webhookSecret")) {
		log.Printf("abacate: bad webhookSecret")
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	if reason := verifyWebhookSignature(r, raw); reason != "" {
		log.Printf("abacate: rejected webhook (%s)", reason)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// AbacatePay's webhook payload shape has drifted across the live docs, the
	// CLI samples and the go-types SDK, so we parse a SUPERSET and use whatever
	// is present rather than betting on one format:
	//   - live v2 docs: event transparent.completed / checkout.completed, charge
	//     under data.transparent / data.checkout, buyer at data.customer.email.
	//   - SDK / CLI:    event billing.paid, charge under data.pixQrCode /
	//     data.billing, no customer object (buyer via externalId / our metadata).
	// Email is resolved from the first source that has it; status/charge from the
	// first charge object present. Action is decided from the event name AND the
	// status so a refund/dispute can't be mistaken for a fresh payment.
	type chargeObj struct {
		ID         string `json:"id"`
		ExternalID string `json:"externalId"`
		Status     string `json:"status"`
		Metadata   struct {
			Email string `json:"email"`
		} `json:"metadata"`
	}
	var p struct {
		Event string `json:"event"`
		Data  struct {
			Transparent *chargeObj `json:"transparent"`
			Checkout    *chargeObj `json:"checkout"`
			PixQrCode   *chargeObj `json:"pixQrCode"`
			Billing     *chargeObj `json:"billing"`
			Customer    struct {
				Email string `json:"email"`
				Name  string `json:"name"`
			} `json:"customer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// Pick whichever charge object the event carried.
	charge := p.Data.Transparent
	for _, c := range []*chargeObj{p.Data.Checkout, p.Data.Billing, p.Data.PixQrCode} {
		if charge == nil {
			charge = c
		}
	}

	// No charge object => payout/transfer/unknown event with no membership
	// impact. Ack so AbacatePay stops retrying, and log it for the record.
	if charge == nil {
		var rawJSON any
		json.Unmarshal(raw, &rawJSON)
		logPaymentEvent(map[string]any{
			"charge_id": "", "event_type": p.Event,
			"charge_status": "", "email": "", "payload": rawJSON,
		})
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ignored")
		return
	}

	chargeID := charge.ID
	status := strings.ToUpper(charge.Status)
	name := p.Data.Customer.Name
	// Email: customer object (live docs) -> charge externalId (checkout) ->
	// our metadata.email -> in-memory binding -> durable binding (PIX, restart).
	email := strings.ToLower(strings.TrimSpace(p.Data.Customer.Email))
	for _, cand := range []string{charge.ExternalID, charge.Metadata.Email} {
		if email == "" {
			email = strings.ToLower(strings.TrimSpace(cand))
		}
	}
	if email == "" {
		email = lookupChargeEmail(chargeID)
	}
	if email == "" {
		email = lookupChargeEmailDB(chargeID)
	}

	// Idempotency: AbacatePay retries webhooks. If we've already handled this
	// exact (charge, event, status) triple, ack and stop. Logging before we act
	// means a stale "PAID" replayed after a refund is seen as a duplicate.
	if eventAlreadyProcessed(chargeID, p.Event, status) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "duplicate")
		return
	}

	// Resolve the buyer BEFORE logging the event. If we can't (e.g. a transient
	// PostgREST error inside lookupChargeEmailDB swallowed the only mapping), we
	// must NOT record the event: doing so would make AbacatePay's retry — the
	// one chance to grant once the DB recovers — short-circuit as a "duplicate"
	// and the paid sale would be lost silently. Returning non-2xx without
	// logging keeps the event replayable.
	if email == "" {
		log.Printf("abacate: could not resolve buyer for charge %q (event %s); asking for retry", chargeID, p.Event)
		http.Error(w, "no buyer email on event", http.StatusServiceUnavailable)
		return
	}

	var rawJSON any
	json.Unmarshal(raw, &rawJSON)
	logPaymentEvent(map[string]any{
		"charge_id": chargeID, "event_type": p.Event,
		"charge_status": status, "email": email, "payload": rawJSON,
	})

	// Decide the membership action from the event name first (a refund event can
	// still carry status PAID), falling back to the status field.
	ev := strings.ToLower(p.Event)
	switch {
	case strings.Contains(ev, "refund") || strings.Contains(ev, "disput") ||
		status == "REFUNDED" || status == "UNDER_DISPUTE" || status == "DISPUTED":
		err = upsertMember(map[string]any{
			"email": email, "status": "refunded",
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	case strings.Contains(ev, "cancel") || strings.Contains(ev, "expire") ||
		status == "CANCELLED" || status == "EXPIRED" || status == "FAILED":
		// Only downgrade if this is the charge that granted access. An abandoned
		// duplicate PIX, or a fresh renewal attempt the buyer never paid,
		// eventually fires EXPIRED — that must NOT revoke a membership granted by
		// a different, paid charge. If there's no member row, or its charge id
		// matches this one, proceed; otherwise leave the membership untouched.
		if cur, exists := memberChargeID(email); exists && chargeID != "" && cur != "" && cur != chargeID {
			log.Printf("abacate: ignoring %s for charge %s — member %s held by charge %s",
				ev, chargeID, email, cur)
			break
		}
		err = upsertMember(map[string]any{
			"email": email, "status": "canceled",
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	case strings.Contains(ev, "complet") || strings.Contains(ev, "paid") ||
		strings.Contains(ev, "renew") || status == "PAID" || status == "APPROVED":
		err = grantAccess(email, name, chargeID)
	default:
		// Unknown/irrelevant: acknowledged + logged, no action.
	}
	if err != nil {
		log.Printf("abacate membership update: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "ok")
}

// ---------------------------------------------------------------------------

func main() {
	cfg = loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic)
	mux.HandleFunc("/lessons.html", handleLessons)
	mux.HandleFunc("/sitemap.xml", handleSitemap)
	mux.HandleFunc("/login", handleLoginPage)
	mux.HandleFunc("/profile", handleProfilePage)
	mux.HandleFunc("/signup", handleSignupPage)
	mux.HandleFunc("/forgot", handleForgotPage)
	mux.HandleFunc("/reset", handleResetPage)
	mux.HandleFunc("/auth/login", handleSignin)
	mux.HandleFunc("/auth/signup", handleSignup)
	mux.HandleFunc("/auth/recover", handleRecover)
	mux.HandleFunc("/auth/reset", handleResetPassword)
	mux.HandleFunc("/auth/callback", handleCallback)
	mux.HandleFunc("/auth/session", handleSession)
	mux.HandleFunc("/auth/logout", handleLogout)
	mux.HandleFunc("/me", handleMe)
	mux.HandleFunc("/join.js", handleJoinJS)
	mux.HandleFunc("/comprar", handleBuy)
	mux.HandleFunc("/pix/new", handlePixNew)
	mux.HandleFunc("/pix/status", handlePixStatus)
	mux.HandleFunc("/card/new", handleCardNew)
	mux.HandleFunc("/coupon/check", handleCouponCheck)
	mux.HandleFunc("/checkout/new", handleCheckoutNew)
	mux.HandleFunc("/protected/", handleProtected)
	mux.HandleFunc("/webhooks/abacatepay", handleAbacateWebhook)

	addr := cfg.BindAddr + ":" + cfg.Port
	log.Printf("navylily auth server on %s (supabase: %s)", addr, cfg.SupabaseURL)
	log.Fatal(http.ListenAndServe(addr, mux))
}
