// Navy Lily — auth + payments server.
//
// Responsibilities (and ONLY these — the rest of the site is static):
//  1. Email+password login via self-hosted Supabase Auth (GoTrue) for the
//     credentials, with our own opaque, revocable sessions on top (the secret's
//     SHA-256 hash is stored in public.auth_sessions; the cookie holds id.secret).
//  2. Create AbacatePay PIX charges and receive their webhooks to
//     grant/revoke paid community membership.
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
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif" // register GIF decoder for image.Decode
	"image/jpeg"  // avatars are stored as small JPEGs
	_ "image/png" // register PNG decoder for image.Decode
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
	"unicode/utf8"
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
	PriceCents       int    // charge amount in cents (497 BRL -> 49700)
	ProductID        string // AbacatePay product id (prod_...) for the hosted card subscription
	OneTimeProductID string // one-time product (prod_...) for coupon/discounted hosted checkouts
	SiteURL          string // public URL of the site, for magic-link redirect
	ProtectedDir     string // filesystem dir with paid lessons (gated; served only to active members at /protected/)
	PublicDir        string // filesystem dir with free lessons (served openly at /)
	AvatarDir        string // filesystem dir where uploaded profile pictures are stored
	PostImageDir     string // filesystem dir where forum post images are stored
	OwnerEmail       string // forum owner; may delete any post/comment (empty = no owner powers)
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
	price, err := strconv.Atoi(env("PRICE_CENTS", "49700"))
	if err != nil || price <= 0 {
		log.Fatalf("PRICE_CENTS must be a positive integer (cents); got %q", env("PRICE_CENTS", "49700"))
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
		AvatarDir:        env("AVATAR_DIR", "data/avatars"),
		PostImageDir:     env("POST_IMAGE_DIR", "data/posts"),
		OwnerEmail:       strings.ToLower(strings.TrimSpace(env("OWNER_EMAIL", ""))),
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

// svcHeaders is the service-role auth pair every PostgREST call uses.
func svcHeaders() map[string]string {
	return map[string]string{
		"apikey":        cfg.ServiceRoleKey,
		"Authorization": "Bearer " + cfg.ServiceRoleKey,
	}
}

// anonHeaders is the GoTrue auth pair: the anon apikey plus a bearer — the anon
// key itself, or a user's access token when one is supplied.
func anonHeaders(bearer string) map[string]string {
	if bearer == "" {
		bearer = cfg.AnonKey
	}
	return map[string]string{"apikey": cfg.AnonKey, "Authorization": "Bearer " + bearer}
}

// restSelect runs a PostgREST GET (service role) and decodes the JSON rows.
// ctx names the call in error messages.
func restSelect[T any](ctx, q string) ([]T, error) {
	resp, err := supabaseDo("GET", q, nil, svcHeaders())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s %d: %s", ctx, resp.StatusCode, b)
	}
	var rows []T
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// restWrite runs a PostgREST write (service role). prefer, when non-empty, is
// sent as the Prefer header. Returns the response body (useful with
// return=representation); a >=300 status becomes an error carrying the body.
func restWrite(ctx, method, path string, body any, prefer string) ([]byte, error) {
	h := svcHeaders()
	if prefer != "" {
		h["Prefer"] = prefer
	}
	resp, err := supabaseDo(method, path, body, h)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %d: %s", ctx, resp.StatusCode, b)
	}
	return b, nil
}

// gotrueRequest sends a GoTrue request whose response body we don't need,
// reporting a non-2xx status as an error carrying the body.
func gotrueRequest(ctx, method, path string, body any) error {
	resp, err := supabaseDo(method, path, body, anonHeaders(""))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %d: %s", ctx, resp.StatusCode, b)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
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
	// GoTrue takes the post-confirmation redirect from the `redirect_to` query
	// param; anything in the request body is ignored and it falls back to the
	// dashboard Site URL (the homepage). Pass it as a query param so the email
	// link lands on /auth/callback, where the token-reading JS lives.
	return gotrueRequest("gotrue otp", "POST",
		"/auth/v1/otp?redirect_to="+url.QueryEscape(redirect),
		map[string]any{"email": email})
}

// userFromToken validates a GoTrue access token and returns the user's id and
// verified email. Used by the link-based flows (email confirmation, the anon
// buyer's one-time login link, password reset) that land a token in the URL
// fragment and post it to us, so we can mint our own session for that user.
func userFromToken(token string) (userID, email string, err error) {
	resp, err := supabaseDo("GET", "/auth/v1/user", nil, anonHeaders(token))
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
	// redirect_to must be a query param; GoTrue ignores it in the body (see
	// sendMagicLink), which is why confirmation links would otherwise land on
	// the homepage instead of /auth/callback.
	return gotrueRequest("gotrue signup", "POST",
		"/auth/v1/signup?redirect_to="+url.QueryEscape(redirect),
		map[string]any{"email": email, "password": password})
}

// resendConfirmation asks GoTrue to send a fresh signup-confirmation link
// (the original may be expired or lost to spam). Same redirect rules as signUp.
func resendConfirmation(email string) error {
	redirect := cfg.SiteURL + "/auth/callback"
	return gotrueRequest("gotrue resend", "POST",
		"/auth/v1/resend?redirect_to="+url.QueryEscape(redirect),
		map[string]any{"type": "signup", "email": email})
}

// errEmailNotConfirmed marks a sign-in that failed only because the account
// hasn't clicked its confirmation link yet — the form shows a "confirm your
// e-mail" message (with a resend button) instead of "wrong password", which
// would otherwise be a dead end for fresh signups.
var errEmailNotConfirmed = errors.New("email not confirmed")

// passwordSignIn verifies an email+password against GoTrue. On success it
// returns the user's id and verified email. GoTrue rejects wrong passwords and
// (when confirmations are on) unconfirmed accounts with a non-200; the
// unconfirmed case is told apart via error_code, the rest stays a generic
// invalid-credentials error so the form can't enumerate accounts.
func passwordSignIn(email, password string) (userID, verifiedEmail string, err error) {
	resp, err := supabaseDo("POST", "/auth/v1/token?grant_type=password",
		map[string]any{"email": email, "password": password}, anonHeaders(""))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		var ge struct {
			ErrorCode string `json:"error_code"`
		}
		json.Unmarshal(b, &ge)
		if ge.ErrorCode == "email_not_confirmed" {
			return "", "", errEmailNotConfirmed
		}
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
	// redirect_to must be a query param; GoTrue ignores it in the body (see
	// sendMagicLink). Without it the recovery link falls back to the dashboard
	// Site URL (the homepage) and never reaches /reset.
	return gotrueRequest("gotrue recover", "POST",
		"/auth/v1/recover?redirect_to="+url.QueryEscape(cfg.SiteURL+"/auth/callback"),
		map[string]any{"email": email})
}

// updatePassword sets a new password for the user identified by a GoTrue access
// token (the recovery token from the reset email link). On a GoTrue rejection it
// also returns the machine-readable error_code (e.g. "same_password") so the
// caller can show a specific message instead of a generic "try a new link".
func updatePassword(accessToken, newPassword string) (code string, err error) {
	resp, err := supabaseDo("PUT", "/auth/v1/user",
		map[string]any{"password": newPassword}, anonHeaders(accessToken))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		var ge struct {
			ErrorCode string `json:"error_code"`
		}
		json.Unmarshal(b, &ge)
		return ge.ErrorCode, fmt.Errorf("gotrue update password %d: %s", resp.StatusCode, b)
	}
	return "", nil
}

// resetErrMsg maps a GoTrue password-update error_code to a friendly Portuguese
// message for the reset form; unknown codes fall back to a generic retry.
func resetErrMsg(code string) string {
	switch code {
	case "same_password":
		return "A nova senha precisa ser diferente da senha atual."
	case "weak_password":
		return "Senha muito fraca. Escolha uma senha mais forte."
	default:
		return "Não foi possível alterar a senha. Tente novamente."
	}
}

// isActiveMember checks the active_members view for this email.
func isActiveMember(email string) (bool, error) {
	rows, err := restSelect[struct{}]("active_members",
		"/rest/v1/active_members?select=email&email=eq."+url.QueryEscape(strings.ToLower(email)))
	return len(rows) > 0, err
}

// isMember is isActiveMember for UI paths where a lookup failure just renders
// as "not a member" (the community write handlers check membership and surface
// the error explicitly instead).
func isMember(email string) bool {
	ok, err := isActiveMember(email)
	if err != nil {
		log.Printf("isActiveMember: %v", err)
	}
	return ok
}

// memberChargeID returns the abacate_charge_id currently on file for email and
// whether a member row exists at all. Used to make sure an expiring/cancelled
// charge only downgrades access if it's the charge that granted it — an
// abandoned duplicate PIX (or a fresh renewal attempt) expiring must not revoke
// a membership granted by a different, paid charge.
func memberChargeID(email string) (chargeID string, exists bool) {
	rows, err := restSelect[struct {
		ChargeID *string `json:"abacate_charge_id"`
	}]("member charge", "/rest/v1/members?select=abacate_charge_id&email=eq."+
		url.QueryEscape(strings.ToLower(email))+"&limit=1")
	if err != nil || len(rows) == 0 {
		return "", false
	}
	if rows[0].ChargeID == nil {
		return "", true
	}
	return *rows[0].ChargeID, true
}

// upsertMember grants or updates paid access keyed by email.
func upsertMember(m map[string]any) error {
	_, err := restWrite("upsert member", "POST", "/rest/v1/members", m, "resolution=merge-duplicates")
	return err
}

// memberUntil returns the paid-access expiry on file for email. found is false
// when there's no member row at all (an account that never had access).
func memberUntil(email string) (until time.Time, found bool, err error) {
	rows, err := restSelect[struct {
		ExpiresAt time.Time `json:"expires_at"`
	}]("member until", "/rest/v1/members?select=expires_at&email=eq."+
		url.QueryEscape(strings.ToLower(email))+"&limit=1")
	if err != nil || len(rows) == 0 {
		return time.Time{}, false, err
	}
	return rows[0].ExpiresAt, true, nil
}

// memberShielded reports whether a cancel/expire/refund webhook must NOT revoke
// this email's access right now. It fails CLOSED, the hard never-revoke rule: it
// returns true (protect) when the member still has unexpired paid-through time
// AND whenever the membership lookup errors — a transient read failure must never
// be the reason a paying member loses access. It returns false only after
// positively confirming there's no row, or the row is already past expires_at (in
// which case the active_members view already hides it, so writing a terminal
// status is merely cosmetic).
func memberShielded(email string) bool {
	until, found, err := memberUntil(email)
	if err != nil {
		log.Printf("memberShielded(%s): %v — not revoking on a read error", email, err)
		return true
	}
	return found && until.After(time.Now().UTC())
}

func logPaymentEvent(ev map[string]any) {
	if _, err := restWrite("payment_events log", "POST", "/rest/v1/payment_events", ev, ""); err != nil {
		log.Printf("%v", err)
	}
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
	rows, err := restSelect[struct{}]("payment_events dedupe",
		"/rest/v1/payment_events?select=id"+
			"&charge_id=eq."+url.QueryEscape(chargeID)+
			"&event_type=eq."+url.QueryEscape(eventType)+
			"&charge_status=eq."+url.QueryEscape(chargeStatus)+"&limit=1")
	if err != nil {
		log.Printf("eventAlreadyProcessed: %v", err)
		return false // fail open: better to risk a re-grant than to drop a sale
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
			"description": "Navy Lily — assinatura anual da comunidade",
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
		"completionUrl": cfg.SiteURL + "/community",
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
	// maxRedeems <= 0 means unlimited (absent in the API → Go zero value), so
	// only enforce a cap when it's a positive number. The old `>= 0` check
	// treated every uncapped coupon as already exhausted.
	if d.MaxRedeems > 0 && d.RedeemsCount >= d.MaxRedeems {
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
	_, err := restWrite("create session", "POST", "/rest/v1/auth_sessions", map[string]any{
		"id":          id,
		"user_id":     userID,
		"email":       strings.ToLower(email),
		"secret_hash": hashSecret(secret),
	}, "")
	if err != nil {
		return "", err
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
	rows, err := restSelect[struct {
		Email      string    `json:"email"`
		SecretHash string    `json:"secret_hash"`
		CreatedAt  time.Time `json:"created_at"`
	}]("auth_sessions", "/rest/v1/auth_sessions?select=email,secret_hash,created_at&id=eq."+
		url.QueryEscape(id)+"&limit=1")
	if err != nil || len(rows) == 0 {
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
	if id != "" {
		restWrite("delete session", "DELETE", "/rest/v1/auth_sessions?id=eq."+url.QueryEscape(id), nil, "")
	}
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
	clearHint(w)
}

// nl_hint is the small, JS-readable companion to the (HttpOnly) session cookie.
// header.js reads it to paint the avatar + profile link immediately on the next
// page load, before /me returns — so a slow link no longer shows the logged-out
// "Profile/login" header for seconds. It carries only public display data, never
// the auth secret: username, whether an avatar exists and its version (to
// cache-bust /avatar/me), and a one-letter fallback initial.
const hintCookie = "nl_hint"

func writeHint(w http.ResponseWriter, p profile) {
	ver := avatarVer(p.AvatarUpdatedAt)
	initial := "?"
	if p.Username != "" {
		initial = strings.ToUpper(p.Username[:1])
	} else if p.Email != "" {
		initial = strings.ToUpper(p.Email[:1])
	}
	b, _ := json.Marshal(map[string]any{"u": p.Username, "a": p.HasAvatar, "v": ver, "i": initial})
	http.SetCookie(w, &http.Cookie{
		Name:     hintCookie,
		Value:    url.QueryEscape(string(b)),
		Path:     "/",
		HttpOnly: false, // header.js must be able to read it
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cfg.SessionTTLDays * 24 * 60 * 60,
	})
}

// setHint loads the profile for email and refreshes the header hint cookie so
// the very next page paints the avatar without waiting on /me. Best-effort: a
// failure just means that page falls back to the /me round-trip.
func setHint(w http.ResponseWriter, email string) {
	if p, err := getProfile(email); err == nil {
		p.Email = email
		writeHint(w, p)
	}
}

func clearHint(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: hintCookie, Value: "", Path: "/", MaxAge: -1})
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
// Profiles — display username (limited edits), short bio, and avatar.
//
// The username can be set once for free, then changed up to maxUsernameChanges
// times for the lifetime of the account; the picture and bio are editable
// freely. Avatar bytes live on disk (cfg.AvatarDir, one small JPEG per email);
// public.profiles only records that one exists + when it last changed, so the
// UI can cache-bust /avatar/me.
// ---------------------------------------------------------------------------

const (
	maxUsernameChanges = 3    // changes allowed AFTER the first (free) pick
	maxBioChars        = 1000 // bio length cap, in characters (matches the form's counter)
	avatarSize         = 128  // square side, in px, of the stored thumbnail
)

// Sentinel errors for the profile edit handlers, mapped to user-facing messages.
var (
	errUsernameFormat  = fmt.Errorf("nome de usuário inválido")
	errUsernameTaken   = fmt.Errorf("nome de usuário já está em uso")
	errUsernameNoEdits = fmt.Errorf("limite de alterações de nome de usuário atingido")
	errBioTooLong      = fmt.Errorf("bio muito longa")
)

type profile struct {
	Email           string     `json:"email"`
	Username        string     `json:"username"`
	UsernameChanges int        `json:"username_changes"`
	Bio             string     `json:"bio"`
	HasAvatar       bool       `json:"has_avatar"`
	AvatarUpdatedAt *time.Time `json:"avatar_updated_at"`
}

// validUsername enforces the username shape (3-20 chars, lowercase a-z, digits,
// underscore). Done by hand to keep the zero-dependency rule (no regexp import).
func validUsername(s string) bool {
	if len(s) < 3 || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

const profileSelect = "select=email,username,username_changes,bio,has_avatar,avatar_updated_at"

// getProfile reads the profile row for email, or a zero-value profile (no
// username yet) if none exists.
func getProfile(email string) (profile, error) {
	email = strings.ToLower(email)
	rows, err := restSelect[profile]("get profile",
		"/rest/v1/profiles?"+profileSelect+"&email=eq."+url.QueryEscape(email)+"&limit=1")
	if err != nil {
		return profile{}, err
	}
	if len(rows) == 0 {
		return profile{Email: email}, nil
	}
	return rows[0], nil
}

// getProfileByUsername reads the profile row for a username (case-insensitive),
// or a zero-value profile if none exists / the name is malformed. Used by the
// public /@username pages, so it never exposes anything the caller shouldn't see
// — the handlers decide which fields to return (never the e-mail).
func getProfileByUsername(username string) (profile, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername(username) {
		return profile{}, nil // malformed -> treat as not found
	}
	// ilike with no wildcards is a case-insensitive exact match, matching the
	// lower(username) unique index.
	rows, err := restSelect[profile]("get profile by username",
		"/rest/v1/profiles?"+profileSelect+"&username=ilike."+url.QueryEscape(username)+"&limit=1")
	if err != nil || len(rows) == 0 {
		return profile{}, err
	}
	return rows[0], nil
}

// upsertProfile writes (insert or merge) a profiles row. A Postgres
// unique-violation (code 23505) means the username is taken; surface that as
// errUsernameTaken so the form can show a precise message.
func upsertProfile(m map[string]any) error {
	_, err := restWrite("upsert profile", "POST", "/rest/v1/profiles", m, "resolution=merge-duplicates")
	if err != nil && strings.Contains(err.Error(), "23505") {
		return errUsernameTaken
	}
	return err
}

// setUsername validates and stores a new username, enforcing the lifetime edit
// cap. The first pick (no username yet) is free; each later change counts toward
// maxUsernameChanges. A no-op (same username) succeeds without spending a change.
func setUsername(email, username string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername(username) {
		return errUsernameFormat
	}
	p, err := getProfile(email)
	if err != nil {
		return err
	}
	if p.Username == username {
		return nil // unchanged — don't spend an edit
	}
	changes := p.UsernameChanges
	if p.Username != "" {
		if changes >= maxUsernameChanges {
			return errUsernameNoEdits
		}
		changes++ // count only changes after the initial free pick
	}
	return upsertProfile(map[string]any{
		"email":            strings.ToLower(email),
		"username":         username,
		"username_changes": changes,
		"updated_at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// setBio stores a bio, rejecting anything over maxBioChars characters.
func setBio(email, bio string) error {
	bio = strings.TrimSpace(bio)
	if utf8.RuneCountInString(bio) > maxBioChars {
		return errBioTooLong
	}
	return upsertProfile(map[string]any{
		"email":      strings.ToLower(email),
		"bio":        bio,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// avatarPath is the on-disk path for an account's avatar JPEG. The filename is a
// hash of the e-mail so the avatars dir never leaks addresses.
func avatarPath(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return filepath.Join(cfg.AvatarDir, hex.EncodeToString(sum[:])+".jpg")
}

// ---------------------------------------------------------------------------
// Lesson completions / profile heatmap
// ---------------------------------------------------------------------------

// recordCompletion marks a lesson done for an account. The (email, lesson_slug)
// primary key makes it idempotent: re-marking the same lesson is ignored, so the
// original completion date — and the heatmap — never shifts. resolution=
// ignore-duplicates turns the conflicting insert into a quiet no-op.
func recordCompletion(email, slug string) error {
	_, err := restWrite("record completion", "POST", "/rest/v1/lesson_completions",
		map[string]any{"email": strings.ToLower(email), "lesson_slug": slug},
		"resolution=ignore-duplicates")
	return err
}

// removeCompletion undoes a completion (the lesson page's "Desfazer" link), in
// case the button was clicked by accident. Deleting a row that isn't there is a
// no-op success, so this is idempotent like recordCompletion.
func removeCompletion(email, slug string) error {
	_, err := restWrite("remove completion", "DELETE",
		"/rest/v1/lesson_completions?email=eq."+url.QueryEscape(strings.ToLower(email))+
			"&lesson_slug=eq."+url.QueryEscape(slug), nil, "")
	return err
}

// getHeatmap returns an account's completion calendar: a "YYYY-MM-DD" -> count
// map (only days with at least one completion) plus the grand total. The frontend
// builds the empty year grid and just looks days up, so we send the sparse data.
func getHeatmap(email string) (days map[string]int, total int, err error) {
	rows, err := restSelect[struct {
		CompletedOn string `json:"completed_on"`
	}]("get heatmap", "/rest/v1/lesson_completions?select=completed_on&email=eq."+
		url.QueryEscape(strings.ToLower(email)))
	if err != nil {
		return nil, 0, err
	}
	days = make(map[string]int, len(rows))
	for _, r := range rows {
		days[r.CompletedOn]++
		total++
	}
	return days, total, nil
}

// isCompleted reports whether an account has already marked a given lesson done.
// Used to paint the lesson page's button in its right state on load.
func isCompleted(email, slug string) (bool, error) {
	rows, err := restSelect[struct{}]("is completed",
		"/rest/v1/lesson_completions?select=lesson_slug&email=eq."+
			url.QueryEscape(strings.ToLower(email))+
			"&lesson_slug=eq."+url.QueryEscape(slug)+"&limit=1")
	return len(rows) > 0, err
}

// getCompletedSlugs returns every lesson slug an account has completed — the
// lesson lists tick those and link "continuar" to the first one still open.
func getCompletedSlugs(email string) ([]string, error) {
	rows, err := restSelect[struct {
		Slug string `json:"lesson_slug"`
	}]("completed slugs", "/rest/v1/lesson_completions?select=lesson_slug&email=eq."+
		url.QueryEscape(strings.ToLower(email)))
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(rows))
	for _, row := range rows {
		slugs = append(slugs, row.Slug)
	}
	return slugs, nil
}

// handleCompletedAPI returns the logged-in account's completed lesson slugs as
// {"slugs": [...]}. 401 when logged out — the lists just skip their progress line.
func handleCompletedAPI(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	slugs, err := getCompletedSlugs(email)
	if err != nil {
		log.Printf("getCompletedSlugs: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"slugs": slugs})
}

// completeWidget renders the per-viewer "mark lesson complete" control that the
// lesson page's <!--COMPLETE--> marker is replaced with. It's a single toggle
// button: one click marks the lesson done, another undoes it. The click is sent
// to /api/complete with fetch (no page reload) and the button re-renders from
// the response. Logged-out visitors get nothing — the feature is members-only,
// and the page is served per-session anyway. slug is the lesson id (e.g. "001" /
// "protected/004"); it's validated by the caller, so it's safe to drop straight
// into the data attribute.
func completeWidget(email, slug string) string {
	if email == "" {
		return ""
	}
	done, err := isCompleted(email, slug)
	if err != nil {
		// Fail open to the "not done" control: a stray re-complete is idempotent
		// and harmless, whereas hiding the control would look broken.
		log.Printf("completeWidget isCompleted: %v", err)
	}
	mark := "🌊" // a completed lesson earns a wave
	state, label, flag := "todo", "Completar aula", "0"
	if done {
		state, label, flag = "done", mark+" Aula concluída", "1"
	}
	return `<div class="lesson-complete" data-slug="` + slug + `">` +
		`<button type="button" class="lesson-complete-btn is-` + state + `" data-done="` + flag + `">` +
		label + `</button></div>` + completeScript
}

// completeScript drives the toggle button: it POSTs the new state to
// /api/complete with fetch and re-renders the button from the JSON reply, so the
// page never reloads. There's exactly one widget per lesson page, and the script
// follows the button in the DOM, so it can bind immediately.
const completeScript = `<script>
(function(){
  var box = document.querySelector('.lesson-complete');
  if(!box) return;
  var btn = box.querySelector('.lesson-complete-btn');
  var slug = box.getAttribute('data-slug');
  var mark = '🌊';
  function render(done){
    btn.classList.toggle('is-done', done);
    btn.classList.toggle('is-todo', !done);
    btn.textContent = done ? mark + ' Aula concluída' : 'Completar aula';
    btn.title = done ? 'Clique para desfazer' : '';
    btn.dataset.done = done ? '1' : '0';
  }
  btn.addEventListener('click', function(){
    var undo = btn.dataset.done === '1';
    btn.disabled = true;
    var body = new URLSearchParams();
    body.set('lesson', slug);
    if(undo) body.set('undo', '1');
    fetch('/api/complete', {method:'POST', headers:{'X-Requested-With':'fetch'}, body: body})
      .then(function(r){ return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function(d){ render(!!d.done); })
      .catch(function(){ alert('Não foi possível salvar. Tente novamente.'); })
      .then(function(){ btn.disabled = false; });
  });
})();
</script>`

// serveLessonHTML serves a lesson page from disk, swapping its <!--COMPLETE-->
// marker for completeWidget. Because the result depends on the session it is
// never cached (browser or Cloudflare edge) — unlike the rest of the lesson
// tree. Pages without the marker are served untouched by the caller.
func serveLessonHTML(w http.ResponseWriter, r *http.Request, file, slug, email string) {
	b, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b = bytes.Replace(b, []byte("<!--COMPLETE-->"), []byte(completeWidget(email, slug)), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(b)
}

// lessonSlug derives a lesson's completion slug from its request path:
//
//	/001.html           -> "001"
//	/protected/004.html -> "protected/004"
//
// matching how recordCompletion keys rows. Returns "" if the path isn't a valid
// lesson slug (so the caller falls back to serving the file unpersonalized).
func lessonSlug(path string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".html")
	if s == "" || len(s) > 64 || !validSlug(s) {
		return ""
	}
	return s
}

// squareThumb center-crops src to a square and nearest-neighbour scales it down
// to size×size. Nearest-neighbour keeps it dependency-free (no x/image) and the
// slight pixelation on a 128px thumbnail is fine for a profile picture.
func squareThumb(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	ox := b.Min.X + (b.Dx()-side)/2
	oy := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		sy := oy + y*side/size
		for x := 0; x < size; x++ {
			dst.Set(x, y, src.At(ox+x*side/size, sy))
		}
	}
	return dst
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
	rows, err := restSelect[struct {
		Email string `json:"email"`
	}]("charge email", "/rest/v1/payment_events?select=email&charge_id=eq."+
		url.QueryEscape(chargeID)+"&email=not.is.null&order=received_at.desc&limit=1")
	if err != nil || len(rows) == 0 {
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setPublicCache(w, 300) // public listing; same for everyone, refreshes in 5 min
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
// explicitly; each page's <lastmod> is its file mtime.
func handleSitemap(w http.ResponseWriter, r *http.Request) {
	lastmod := func(f string) string {
		info, err := os.Stat(f)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("<lastmod>%s</lastmod>", info.ModTime().UTC().Format("2006-01-02"))
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	fmt.Fprintf(&b, "  <url><loc>%s/</loc>%s<changefreq>weekly</changefreq><priority>1.0</priority></url>\n",
		cfg.SiteURL, lastmod(filepath.Join(cfg.PublicDir, "root.html")))
	files, _ := filepath.Glob(filepath.Join(cfg.PublicDir, "*.html"))
	for _, f := range files {
		base := filepath.Base(f)
		// root.html IS "/" and wiki.html IS "/wiki" (handleStatic 301s the
		// .html spellings), so only the canonical URLs are listed.
		if base == "root.html" || base == "wiki.html" {
			continue
		}
		fmt.Fprintf(&b, "  <url><loc>%s/%s</loc>%s<changefreq>weekly</changefreq><priority>0.8</priority></url>\n",
			cfg.SiteURL, base, lastmod(f))
	}
	fmt.Fprintf(&b, "  <url><loc>%s/wiki</loc>%s<changefreq>weekly</changefreq><priority>0.8</priority></url>\n",
		cfg.SiteURL, lastmod(filepath.Join(cfg.PublicDir, "wiki.html")))
	wiki, _ := filepath.Glob(filepath.Join(cfg.PublicDir, "wiki", "*.html"))
	for _, f := range wiki {
		fmt.Fprintf(&b, "  <url><loc>%s/wiki/%s</loc>%s<changefreq>weekly</changefreq><priority>0.6</priority></url>\n",
			cfg.SiteURL, filepath.Base(f), lastmod(f))
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	setPublicCache(w, 3600) // public, regenerated cheaply; an hour at the edge is plenty
	w.Write([]byte(b.String()))
}

// handleRobots serves the origin robots.txt. Cloudflare prepends its managed
// "content signals" block (AI-crawler rules) at the edge; this origin part is
// what search engines act on — everything crawlable, plus the Sitemap pointer
// Google uses to discover new lessons.
func handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	setPublicCache(w, 3600)
	fmt.Fprintf(w, "User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n", cfg.SiteURL)
}

// serveHTMLNoStore serves an HTML page from disk with Cache-Control: no-store.
// Auth/app pages must never be served stale: a cached reset.html or callback.html
// keeps running old JS in the browser (and at the Cloudflare edge) even after a
// deploy, which is exactly how a fixed reset page can keep showing the old error.
func serveHTMLNoStore(w http.ResponseWriter, r *http.Request, file string) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, file)
}

// setPublicCache marks a response cacheable by browsers and shared CDNs
// (Cloudflare), so it can be served from an edge PoP instead of traversing the
// home uplink on every hit. Use ONLY for public, non-personalized responses
// that never depend on the session cookie and never set one — never for /me,
// /api/*, the private /avatar/me, or any logged-in page.
func setPublicCache(w http.ResponseWriter, seconds int) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", seconds))
}

// page serves one of the auth/app HTML files with no-store (see
// serveHTMLNoStore). These pages gate themselves client-side off /me where
// needed (the session cookie never reaches static files).
func page(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { serveHTMLNoStore(w, r, file) }
}

// servePageWithErro serves a form page, substituting its <!--ERRO--> marker
// with a server-rendered error block. Rendering errors server-side (instead of
// un-hiding a <p> with inline JS) keeps failures visible without JavaScript
// and lets role="alert" announce them to screen readers.
func servePageWithErro(w http.ResponseWriter, r *http.Request, file, erroHTML string) {
	b, err := os.ReadFile(file)
	if err != nil {
		log.Printf("servePageWithErro %s: %v", file, err)
		http.Error(w, "página indisponível", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(bytes.Replace(b, []byte("<!--ERRO-->"), []byte(erroHTML), 1))
}

func erroP(msg string) string {
	return `<p id="erro" role="alert" style="color:#b00;">` + msg + `</p>`
}

// handleLoginPage serves the login form. Visitors who are already logged in
// are sent on to their destination — the header "Perfil" button points here,
// so a member on a slow connection (or without JS) must not land back on a
// form they've already passed.
func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if currentEmail(r) != "" {
		dest := sanitizeNext(r.URL.Query().Get("next"))
		if dest == "" {
			dest = "/profile"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	var msg string
	switch r.URL.Query().Get("erro") {
	case "":
	case "confirma":
		msg = erroP("Sua conta ainda não foi confirmada. Abra o link que enviamos por e-mail (confira também o spam).") +
			resendFormHTML(r.URL.Query().Get("email"))
	default:
		msg = erroP("E-mail ou senha incorretos.")
	}
	servePageWithErro(w, r, "web/login.html", msg)
}

// handleAfterLogin sends a just-logged-in user to their home: the lesson index.
// The whole course is free now, so there's nothing to gate — everyone lands on
// the lessons. Link-based logins (e-mail confirmation, password reset) use it
// as their default destination.
func handleAfterLogin(w http.ResponseWriter, r *http.Request) {
	dest := "/login"
	if currentEmail(r) != "" {
		dest = "/lessons.html"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// resendFormHTML renders the one-button "resend confirmation e-mail" form
// shown under the unconfirmed-account login error.
func resendFormHTML(email string) string {
	if !strings.Contains(email, "@") {
		return ""
	}
	return `<form method="POST" action="/auth/resend"><input type="hidden" name="email" value="` +
		html.EscapeString(email) + `"><button type="submit">Reenviar e-mail de confirmação</button></form>`
}

// handleSignupPage serves the signup form, with the same server-rendered
// errors and logged-in redirect as handleLoginPage.
func handleSignupPage(w http.ResponseWriter, r *http.Request) {
	if currentEmail(r) != "" {
		dest := sanitizeNext(r.URL.Query().Get("next"))
		if dest == "" {
			dest = "/profile"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	var msg string
	switch r.URL.Query().Get("erro") {
	case "":
	case "email":
		msg = erroP("Digite um e-mail válido.")
	case "senha":
		msg = erroP("As senhas devem ter entre 8 e 72 caracteres e ser iguais.")
	default:
		// The most common cause is an e-mail that already has an account —
		// e.g. a gifted legacy member who already registered. Don't dead-end
		// them on a generic failure: point to login and password reset. Phrased
		// conditionally so it doesn't confirm whether the account exists.
		email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
		q := ""
		if strings.Contains(email, "@") {
			q = "?email=" + url.QueryEscape(email)
		}
		msg = erroP(`Não foi possível criar a conta. Se você já tem cadastro com este e-mail, ` +
			`<a href="/login` + q + `">entre</a> ou ` +
			`<a href="/forgot` + q + `">redefina sua senha</a>.`)
	}
	servePageWithErro(w, r, "web/signup.html", msg)
}

// script serves a static JS asset: same bytes for everyone, edge-cacheable.
func script(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		setPublicCache(w, 3600)
		http.ServeFile(w, r, file)
	}
}

// handleStatic serves the free, open lessons and other static pages from
// PublicDir. It is the catch-all ("/") route; the served tree is rooted at
// PublicDir, so a request can only ever reach public, free content.
func handleStatic(w http.ResponseWriter, r *http.Request) {
	// filepath.Clean on a rooted path collapses any ../ so requests can't
	// escape PublicDir.
	// Public vanity profiles live at /@username. Serve the shell here (it reads
	// the name from the path and fetches /api/u); keep it ahead of the file
	// lookup so a literal "@..." path never hits the static tree.
	if strings.HasPrefix(r.URL.Path, "/@") {
		serveHTMLNoStore(w, r, "web/u.html")
		return
	}
	clean := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	// One URL per page: the .html spellings of the landing page and wiki index
	// 301 to their canonical paths, so search engines don't index duplicates.
	if clean == "/root.html" {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return
	}
	if clean == "/wiki.html" {
		http.Redirect(w, r, "/wiki", http.StatusMovedPermanently)
		return
	}
	if clean == "/" {
		// Landing page: the headline + lesson menu built by parser.sh.
		setPublicCache(w, 300)
		http.ServeFile(w, r, filepath.Join(cfg.PublicDir, "root.html"))
		return
	}
	if clean == "/wiki" {
		// Wiki index (parser.sh builds it next to the wiki/ entry pages, which
		// the directory check below would otherwise 404).
		setPublicCache(w, 300)
		http.ServeFile(w, r, filepath.Join(cfg.PublicDir, "wiki.html"))
		return
	}
	full := filepath.Join(cfg.PublicDir, clean)
	if info, err := os.Stat(full); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// Lesson pages carry the per-viewer "completar aula" control, rendered into
	// the <!--COMPLETE--> marker from the session. That makes them personalized,
	// so they're served uncached (serveLessonHTML sets no-store).
	if slug := lessonSlug(clean); slug != "" {
		serveLessonHTML(w, r, full, slug, currentEmail(r))
		return
	}
	// The rest of PublicDir is free, public, non-personalized content (the header
	// is personalized client-side via header.js + /me, neither of which is
	// cached). Cache assets aggressively; keep HTML short so edits show up fast.
	// The service worker must revalidate every load — a day-cached sw.js would
	// pin clients to stale caching logic. The search index follows the lessons.
	switch {
	case clean == "/sw.js":
		w.Header().Set("Cache-Control", "no-cache")
	// PDFs are rebuilt monthly under the same URL, so they can't ride the
	// day-long asset cache — five minutes keeps the newest edition flowing.
	// They download under a month-stamped name so each edition saves as a
	// new file instead of tripping the browser's "download again?" prompt.
	case strings.HasSuffix(clean, ".pdf"):
		setPublicCache(w, 300)
		setPDFDownloadName(w, clean)
	case strings.HasSuffix(clean, ".html") || clean == "/search.txt":
		setPublicCache(w, 300)
	default:
		setPublicCache(w, 86400)
	}
	http.ServeFile(w, r, full)
}

// setPDFDownloadName makes a course PDF download as an attachment named after
// the file plus the current month — "curso gratuito junho 2026.pdf" — so every
// monthly edition lands as a distinct file in the user's downloads folder.
// Month names are kept ASCII ("marco") to stay inside the plain quoted-string
// syntax of Content-Disposition.
func setPDFDownloadName(w http.ResponseWriter, clean string) {
	months := [...]string{"janeiro", "fevereiro", "marco", "abril", "maio", "junho",
		"julho", "agosto", "setembro", "outubro", "novembro", "dezembro"}
	now := time.Now()
	base := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(clean), ".pdf"), "-", " ")
	name := fmt.Sprintf("%s %s %d.pdf", base, months[now.Month()-1], now.Year())
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
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

// emailQS renders an &email=... query fragment (or ""), used to carry the
// typed e-mail back through an error redirect so the form re-fills it instead
// of making the user retype everything.
func emailQS(email string) string {
	if !strings.Contains(email, "@") {
		return ""
	}
	return "&email=" + url.QueryEscape(email)
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
		http.Redirect(w, r, "/login?erro=1"+emailQS(email)+nextQS(next), http.StatusSeeOther)
		return
	}
	userID, vEmail, err := passwordSignIn(email, password)
	if err != nil {
		// Unconfirmed accounts get their own message (with a resend button)
		// instead of a misleading "wrong password"; either way the typed e-mail
		// rides back so only the password needs retyping.
		code := "1"
		if errors.Is(err, errEmailNotConfirmed) {
			code = "confirma"
		}
		http.Redirect(w, r, "/login?erro="+code+emailQS(email)+nextQS(next), http.StatusSeeOther)
		return
	}
	token, err := createServerSession(userID, vEmail)
	if err != nil {
		log.Printf("createServerSession (signin): %v", err)
		http.Error(w, "não foi possível entrar agora", http.StatusBadGateway)
		return
	}
	setSession(w, token)
	setHint(w, vEmail)
	dest := next
	if dest == "" {
		// The whole course is free, so everyone lands on the lesson index —
		// login must never feel like a paywall.
		dest = "/lessons.html"
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
		http.Redirect(w, r, "/signup?erro=senha"+emailQS(email)+nextQS(next), http.StatusSeeOther)
		return
	}
	if err := signUp(email, password, next); err != nil {
		// GoTrue rejects e.g. weak/breached passwords or an already-registered
		// e-mail. Keep the reason generic so the form can't enumerate accounts.
		log.Printf("signUp: %v", err)
		http.Redirect(w, r, "/signup?erro=falha"+emailQS(email)+nextQS(next), http.StatusSeeOther)
		return
	}
	// Redirect (PRG) instead of serving the page on the POST: otherwise a
	// refresh re-submits the form and greets the brand-new account with
	// "e-mail pode já estar em uso".
	http.Redirect(w, r, "/check-email", http.StatusSeeOther)
}

// handleResend re-sends the signup confirmation e-mail (the button under the
// "account not confirmed" login error). Like /auth/recover it always lands on
// /check-email and rate-limits per address, so it can't enumerate accounts.
func handleResend(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if strings.Contains(email, "@") && allowMagicLink(email) {
		if err := resendConfirmation(email); err != nil {
			log.Printf("resendConfirmation: %v", err)
		}
	}
	http.Redirect(w, r, "/check-email", http.StatusSeeOther)
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
	// PRG: land on a GET page so a refresh doesn't re-submit the form.
	http.Redirect(w, r, "/check-email", http.StatusSeeOther)
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
	setHint(w, email)
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
		http.Error(w, "Link inválido ou expirado.", http.StatusUnauthorized)
		return
	}
	if code, err := updatePassword(body.AccessToken, body.Password); err != nil {
		log.Printf("updatePassword: %v", err)
		http.Error(w, resetErrMsg(code), http.StatusBadRequest)
		return
	}
	token, err := createServerSession(userID, email)
	if err != nil {
		log.Printf("createServerSession (reset): %v", err)
		http.Error(w, "session error", http.StatusBadGateway)
		return
	}
	setSession(w, token)
	setHint(w, email)
	w.WriteHeader(http.StatusNoContent)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleMe powers the UI (including the header avatar on every page): who am I,
// am I a paying member, and what's my username / avatar version (for cache-bust).
func handleMe(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	out := map[string]any{"logged_in": email != "", "email": email, "member": false}
	if email != "" {
		out["member"] = isMember(email)
		out["owner"] = isOwner(email)
		if p, err := getProfile(email); err == nil {
			out["username"] = p.Username
			out["has_avatar"] = p.HasAvatar
			out["avatar_ver"] = avatarVer(p.AvatarUpdatedAt)
			p.Email = email
			writeHint(w, p) // keep the optimistic-header hint fresh for the next page
		}
	} else {
		clearHint(w) // session gone: stop the header painting a stale avatar
	}
	writeJSON(w, out)
}

// handleProfileAPI returns the logged-in user's full profile for the account
// page. 401 when logged out so the page can bounce to /login.
func handleProfileAPI(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, err := getProfile(email)
	if err != nil {
		log.Printf("getProfile: %v", err)
		http.Error(w, "erro ao carregar perfil", http.StatusBadGateway)
		return
	}
	out := map[string]any{
		"email":        email,
		"username":     p.Username,
		"username_set": p.Username != "",
		"edits_left":   maxUsernameChanges - p.UsernameChanges,
		"bio":          p.Bio,
		"has_avatar":   p.HasAvatar,
		"avatar_ver":   avatarVer(p.AvatarUpdatedAt),
		"member":       isMember(email),
		"max_chars":    maxBioChars,
	}
	// Access expiry, when a member row exists (paid or gift): the page shows
	// "acesso até <data>" while active and "expirou em <data>" after.
	if until, found, err := memberUntil(email); err != nil {
		log.Printf("memberUntil: %v", err) // non-fatal: the page just omits the date
	} else if found {
		out["member_until"] = until.UTC().Format(time.RFC3339)
	}
	writeJSON(w, out)
}

// profileErrStatus maps a profile sentinel error to an HTTP status.
func profileErrStatus(err error) int {
	switch err {
	case errUsernameFormat, errBioTooLong:
		return http.StatusBadRequest
	case errUsernameTaken:
		return http.StatusConflict
	case errUsernameNoEdits:
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

// handleUsername sets/changes the logged-in user's username (subject to the
// lifetime edit cap). Errors come back as plain text the form shows inline.
func handleUsername(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The form posts a multipart FormData body. Don't call r.ParseForm() here:
	// it doesn't read a multipart body but does set r.Form non-nil, which makes
	// the later r.FormValue skip its own multipart parse and return "". Let
	// FormValue do the parsing instead (it handles both encodings).
	if err := setUsername(email, r.FormValue("username")); err != nil {
		if profileErrStatus(err) == http.StatusBadGateway {
			log.Printf("setUsername: %v", err)
		}
		http.Error(w, err.Error(), profileErrStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBio updates the logged-in user's bio (<= maxBioChars characters).
func handleBio(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// See handleUsername: the bio form posts multipart FormData, so let
	// r.FormValue parse it. A preceding r.ParseForm() would leave bio empty.
	if err := setBio(email, r.FormValue("bio")); err != nil {
		if profileErrStatus(err) == http.StatusBadGateway {
			log.Printf("setBio: %v", err)
		}
		http.Error(w, err.Error(), profileErrStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAvatarUpload accepts a multipart image, downsizes it to a small square
// JPEG and stores it on disk, then flags has_avatar on the profile. Any common
// raster format (PNG/JPEG/GIF) is accepted; the output is always a tiny JPEG.
func handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Cap the upload so a huge file can't exhaust memory while decoding.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	file, _, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "envie uma imagem", http.StatusBadRequest)
		return
	}
	defer file.Close()
	src, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, "imagem inválida (use PNG, JPEG ou GIF)", http.StatusBadRequest)
		return
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, squareThumb(src, avatarSize), &jpeg.Options{Quality: 80}); err != nil {
		log.Printf("avatar encode: %v", err)
		http.Error(w, "não foi possível processar a imagem", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(avatarPath(email), buf.Bytes(), 0o644); err != nil {
		log.Printf("avatar write: %v", err)
		http.Error(w, "não foi possível salvar a imagem", http.StatusInternalServerError)
		return
	}
	if err := upsertProfile(map[string]any{
		"email":             strings.ToLower(email),
		"has_avatar":        true,
		"avatar_updated_at": time.Now().UTC().Format(time.RFC3339),
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("avatar flag: %v", err)
		http.Error(w, "não foi possível salvar a imagem", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAvatarMe serves the logged-in user's avatar JPEG. 404 when none exists
// so the header script falls back to an initial-letter placeholder.
func handleAvatarMe(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.NotFound(w, r)
		return
	}
	p := avatarPath(email)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	// Private + revalidate: callers cache-bust with ?v=<avatar_ver>, so a stale
	// picture is never shown, but a shared proxy must not cache one user's avatar.
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, p)
}

// computeStreak returns the number of consecutive days (ending today or
// yesterday local time) in which the account completed at least one lesson.
func computeStreak(days map[string]int) int {
	today := time.Now().Format("2006-01-02")
	streak := 0
	// Start checking from today; if today has no completion, start from yesterday
	// (streak is still "alive" until the end of today).
	cur, _ := time.Parse("2006-01-02", today)
	if days[today] == 0 {
		cur = cur.AddDate(0, 0, -1)
	}
	for {
		key := cur.Format("2006-01-02")
		if days[key] == 0 {
			break
		}
		streak++
		cur = cur.AddDate(0, 0, -1)
	}
	return streak
}

// handlePublicProfileAPI powers the public /@username page. It returns ONLY the
// public fields — username, bio, avatar version, membership — and NEVER the
// e-mail. 404 when no such username exists so the page can show "not found".
func handlePublicProfileAPI(w http.ResponseWriter, r *http.Request) {
	p, err := getProfileByUsername(r.URL.Query().Get("name"))
	if err != nil {
		log.Printf("getProfileByUsername: %v", err)
		http.Error(w, "erro ao carregar perfil", http.StatusBadGateway)
		return
	}
	if p.Username == "" {
		http.Error(w, "usuário não encontrado", http.StatusNotFound)
		return
	}
	days, _, err := getHeatmap(p.Email)
	streak := 0
	if err != nil {
		log.Printf("getHeatmap (streak): %v", err) // non-fatal: streak stays 0
	} else {
		streak = computeStreak(days)
	}
	writeJSON(w, map[string]any{
		"username":   p.Username,
		"bio":        p.Bio,
		"has_avatar": p.HasAvatar,
		"avatar_ver": avatarVer(p.AvatarUpdatedAt),
		"member":     isMember(p.Email),
		"streak":     streak,
		"is_owner":   currentEmail(r) == p.Email,
	})
}

// handleHeatmapAPI powers the public profile heatmap. Given ?name=<username> it
// returns that account's completion calendar — day counts and a total, NOTHING
// that identifies the person (no e-mail, no lesson slugs). 404 for unknown users
// so the profile page can skip the section. The page lazy-loads this only once
// the heatmap scrolls into view.
func handleHeatmapAPI(w http.ResponseWriter, r *http.Request) {
	p, err := getProfileByUsername(r.URL.Query().Get("name"))
	if err != nil {
		log.Printf("getProfileByUsername (heatmap): %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if p.Username == "" {
		http.Error(w, "usuário não encontrado", http.StatusNotFound)
		return
	}
	days, total, err := getHeatmap(p.Email)
	if err != nil {
		log.Printf("getHeatmap: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"total": total, "days": days})
}

// handleCompleteLesson backs the lesson page's "completar aula" form, submitted
// by the logged-in account. It's a plain POST (no JavaScript): "undo" present
// removes the completion, otherwise it records one, then redirects back to the
// lesson (Post/Redirect/Get) so a refresh doesn't re-submit. Both writes are
// idempotent, so a double submit is harmless. It's the only writer of
// lesson_completions.
func handleCompleteLesson(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	slug := strings.TrimSpace(r.FormValue("lesson"))
	// Keep the slug to a small, safe shape (lesson ids like "001" or
	// "protected/004"); reject anything else so the column stays clean.
	if slug == "" || len(slug) > 64 || !validSlug(slug) {
		http.Error(w, "lição inválida", http.StatusBadRequest)
		return
	}
	undo := r.FormValue("undo") != ""
	var err error
	if undo {
		err = removeCompletion(email, slug)
	} else {
		err = recordCompletion(email, slug)
	}
	if err != nil {
		log.Printf("completeLesson (undo=%v): %v", undo, err)
		http.Error(w, "não foi possível salvar", http.StatusBadGateway)
		return
	}
	// The toggle button calls this with fetch and re-renders from the JSON reply
	// (no reload). A plain form POST (no JS) still works: redirect back to the
	// lesson — /001.html or /protected/004.html — so a refresh doesn't re-submit.
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"done":%t}`, !undo)
		return
	}
	http.Redirect(w, r, "/"+slug+".html", http.StatusSeeOther)
}

// validSlug allows lesson identifiers: lowercase letters, digits, '/', '-', '_'.
// Hand-rolled to keep the zero-dependency rule (no regexp), like validUsername.
func validSlug(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '/' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// handlePublicAvatar serves a user's avatar by username for the public profile
// page (/avatar/u/<username>). 404 (so the page falls back to an initial) when
// the user or picture doesn't exist.
func handlePublicAvatar(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/avatar/u/")
	p, err := getProfileByUsername(name)
	if err != nil || p.Email == "" || !p.HasAvatar {
		http.NotFound(w, r)
		return
	}
	path := avatarPath(p.Email)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	// Public picture: cacheable by shared proxies (callers cache-bust with ?v=).
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// handleProtected gates the paid lessons (the "Art Sovereignty" course): only a
// logged-in, active member may read /protected/*. Anonymous visitors and
// lapsed/never-paid accounts are sent to checkout. Members get the file from
// cfg.ProtectedDir; lesson pages go through serveLessonHTML so the "completar
// aula" widget renders, exactly like the free lessons. Paid content is never
// cached at the edge.
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
		// Logged in but hasn't bought (or it lapsed) -> checkout.
		http.Redirect(w, r, "/comprar", http.StatusSeeOther)
		return
	}
	// Serve the requested file from the protected dir, safely (filepath.Clean on
	// a rooted path collapses any ../ so requests can't escape ProtectedDir).
	rel := strings.TrimPrefix(r.URL.Path, "/protected/")
	if rel == "" {
		rel = "index.html"
	}
	clean := filepath.Clean("/" + rel)
	full := filepath.Join(cfg.ProtectedDir, clean)
	if info, err := os.Stat(full); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// Lesson pages carry the per-viewer completion control; serve them like the
	// free lessons (no-store, <!--COMPLETE--> swapped). Slug keys match the
	// "protected/NNN" form recordCompletion writes.
	if slug := lessonSlug("/protected" + clean); slug != "" {
		serveLessonHTML(w, r, full, slug, email)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, full)
}

// handleBuy serves the checkout page, logged in or not. Anonymous buyers type
// the e-mail that receives access right on the page — the same no-login flow
// as the inline Join widget; a login wall here just cost the sale. The page
// itself fetches /pix/new and polls /pix/status.
func handleBuy(w http.ResponseWriter, r *http.Request) {
	serveHTMLNoStore(w, r, "web/comprar.html")
}

// handlePixNew opens a fresh PIX charge for the logged-in buyer and returns the
// QR (brCodeBase64) + copy-paste code (brCode) as JSON for the checkout page.
func handlePixNew(w http.ResponseWriter, r *http.Request) {
	// POST-only: this opens a real PIX charge. A GET (an <img>/<a> a third-party
	// site could embed) must not be able to spin up charges in a visitor's name.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	writeJSON(w, map[string]any{
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
	writeJSON(w, map[string]any{"valid": couponValid(r.URL.Query().Get("code"))})
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
		// No session and no usable ?email=: back to the checkout page, which
		// asks for the address — login is not required to buy.
		http.Redirect(w, r, "/comprar", http.StatusSeeOther)
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
	// Access is granted to the e-mail this charge was BOUND to when it was created
	// — never the caller's session — so nobody (anonymous OR logged in) can claim
	// someone else's paid charge just by polling its id. The in-memory binding is
	// set at creation; the durable payment_events binding covers a server restart
	// between create and poll. loggedIn only decides whether to mail a login link.
	loggedIn := currentEmail(r)
	email := lookupChargeEmail(id)
	if email == "" {
		email = lookupChargeEmailDB(id)
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
			// allowMagicLink rate-limits per address so a third party polling a
			// known paid charge id can't fan a burst of mails at the buyer
			// (magicSent already caps per-id; this also caps per-email).
			if mailed && allowMagicLink(email) {
				if err := sendMagicLink(email, "/community"); err != nil {
					log.Printf("sendMagicLink (post-pix): %v", err)
				}
			}
		}
	}
	writeJSON(w, map[string]any{
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
	// Extend from whichever is later — the existing expiry or now — so an early
	// renewal stacks the new year on top of the remaining time instead of
	// discarding it. A lapsed/absent membership just gets now()+1y.
	base := time.Now().UTC()
	if until, found, err := memberUntil(email); err != nil {
		log.Printf("grantAccess memberUntil(%s): %v — extending from now", email, err)
	} else if found && until.After(base) {
		base = until.UTC()
	}
	return upsertMember(map[string]any{
		"email":             strings.ToLower(email),
		"name":              name,
		"status":            "active",
		"abacate_charge_id": chargeID,
		"source":            "abacatepay",
		"expires_at":        base.AddDate(1, 0, 0).Format(time.RFC3339),
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
	// logEvent records this webhook in payment_events. It doubles as the
	// idempotency marker (eventAlreadyProcessed reads it back), so we call it only
	// once the event is actually HANDLED — after a successful membership action, or
	// in the "unverified" forgery branch. Logging before acting would let a
	// transient failure get deduped away on AbacatePay's retry and silently lose a
	// paid sale (card has no /pix/status poll fallback). Same reasoning as the
	// email == "" guard above: don't record what we haven't handled, so it stays
	// replayable.
	logEvent := func() {
		logPaymentEvent(map[string]any{
			"charge_id": chargeID, "event_type": p.Event,
			"charge_status": status, "email": email, "payload": rawJSON,
		})
	}

	// Decide the membership action from the event name first (a refund event can
	// still carry status PAID), falling back to the status field.
	ev := strings.ToLower(p.Event)
	switch {
	case strings.Contains(ev, "refund") || strings.Contains(ev, "disput") ||
		status == "REFUNDED" || status == "UNDER_DISPUTE" || status == "DISPUTED":
		// Never claw back access a member still has paid time for, and never revoke
		// on a read error: memberShielded fails closed (the hard never-revoke rule).
		// A refund/chargeback only lands once the paid-through date has already
		// lapsed — by then the active_members view hides the row anyway, so the
		// status write below is cosmetic.
		if memberShielded(email) {
			log.Printf("abacate: %s for charge %s — not revoking %s (paid time remains or lookup failed)",
				ev, chargeID, email)
			break
		}
		// Still skip a refund aimed at an OLD, superseded charge rather than the one
		// on file (e.g. the buyer re-bought with a different, paid charge).
		if cur, exists := memberChargeID(email); exists && chargeID != "" && cur != "" && cur != chargeID {
			log.Printf("abacate: ignoring %s for charge %s — member %s held by charge %s",
				ev, chargeID, email, cur)
			break
		}
		err = upsertMember(map[string]any{
			"email": email, "status": "refunded",
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	case strings.Contains(ev, "cancel") || strings.Contains(ev, "expire") ||
		status == "CANCELLED" || status == "EXPIRED" || status == "FAILED":
		// A cancellation/expiry must not claw back access the buyer already paid
		// for: a cancelled card subscription simply stops auto-renewing, and access
		// runs out the paid-through date (expires_at). memberShielded fails closed —
		// it also protects on a read error, so a transient PostgREST failure during
		// an EXPIRED event can never revoke a paying member (the never-revoke rule).
		// The active_members view already hides expired rows, so leaving a still-paid
		// row active to lapse naturally is the correct behaviour.
		if memberShielded(email) {
			log.Printf("abacate: %s for charge %s — keeping %s (paid time remains or lookup failed)",
				ev, chargeID, email)
			break
		}
		// Past here the row is confirmed expired/absent. Still skip an expiry from
		// an abandoned duplicate or unpaid renewal charge that isn't the one on file.
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
		// Defense in depth against a forged/replayed "paid" webhook (the URL
		// secret is the only mandatory auth — the HMAC header is optional): for an
		// inline PIX charge, re-fetch the authoritative status from AbacatePay and
		// refuse to grant when it positively reports the charge is NOT paid. We
		// only block on a confirmed contradiction — if the lookup errors or the id
		// isn't a transparent charge (card subscriptions/hosted checkouts don't
		// answer /transparents/check), we fall through and trust the verified
		// webhook, so legitimate card/coupon grants are never dropped.
		if chargeID != "" {
			if live, lerr := checkPixStatus(chargeID); lerr == nil &&
				live != "PAID" && live != "APPROVED" {
				// Ack (don't 4xx/5xx) and record it: logging this contradicted/forged
				// attempt makes a retry of the same (charge,event,status) triple
				// self-dedupe so it never re-reaches this grant. If the charge is
				// genuinely paid later, the /pix/status poll grants access out of band.
				log.Printf("abacate: refusing grant for charge %s — webhook said %s but live status is %s",
					chargeID, status, live)
				logEvent()
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "unverified")
				return
			}
		}
		err = grantAccess(email, name, chargeID)
	default:
		// Unknown/irrelevant: acknowledged + logged, no action.
	}
	if err != nil {
		// Do NOT log the event on failure: leaving it unrecorded means AbacatePay's
		// retry isn't seen as a duplicate, so the action gets another chance instead
		// of the paid sale being lost.
		log.Printf("abacate membership update: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	logEvent()
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "ok")
}

// ---------------------------------------------------------------------------
// Community forum: short text+image posts with one level of comments. Reads are
// public; posting, commenting and deleting require an active member (or the
// owner). Like the rest of the server the browser never touches Supabase — these
// JSON endpoints (called by web/community.html) talk to PostgREST with the
// service-role key, and post images are stored on disk (cfg.PostImageDir) the way
// avatars are. The read views join the author's PUBLIC profile and omit the
// e-mail, so an address is never exposed; the delete handlers read author_email
// from the base tables to authorize.
// ---------------------------------------------------------------------------

const (
	maxPostChars    = 2000 // post body length cap, in characters (markdown source)
	maxCommentChars = 1000 // comment length cap, in characters
	postImageMax    = 1280 // longest side, in px, of a stored post image
	feedPageSize    = 30   // posts per feed request ("load more" pages backward)
	dailyPostLimit  = 30   // posts allowed per author per Brazil-day (light spam cap)
)

// Sentinel error from canPost, mapped to a user-facing message by writeCanPostErr.
var errNoUsername = fmt.Errorf("escolha um nome de usuário no seu perfil antes de postar")

// forumPost is a row of the public forum_feed view: the author's handle + avatar
// info and a comment count, never the author's e-mail.
type forumPost struct {
	ID           int64      `json:"id"`
	Board        string     `json:"board"`
	Body         string     `json:"body"`
	HasImage     bool       `json:"has_image"`
	CreatedAt    time.Time  `json:"created_at"`
	AuthorUser   string     `json:"author_username"`
	AuthorAvatar bool       `json:"author_has_avatar"`
	AuthorAvAt   *time.Time `json:"author_avatar_updated_at"`
	CommentCount int        `json:"comment_count"`
	LikeCount    int        `json:"like_count"`
	AuthorMember bool       `json:"author_is_member"` // author is a paying Navy member (badge + owner queue)
}

// forumComment is a row of the public forum_comments_view.
type forumComment struct {
	ID              int64      `json:"id"`
	PostID          int64      `json:"post_id"`
	Body            string     `json:"body"`
	CreatedAt       time.Time  `json:"created_at"`
	AuthorUser      string     `json:"author_username"`
	AuthorAvatar    bool       `json:"author_has_avatar"`
	AuthorAvAt      *time.Time `json:"author_avatar_updated_at"`
	LikeCount       int        `json:"like_count"`
	ParentCommentID *int64     `json:"parent_comment_id"`
	ReplyToUsername string     `json:"reply_to_username"`
}

// isOwner reports whether email is the configured forum owner (OWNER_EMAIL), who
// may delete any post or comment. An empty OWNER_EMAIL disables owner powers.
func isOwner(email string) bool {
	return cfg.OwnerEmail != "" && strings.EqualFold(strings.TrimSpace(email), cfg.OwnerEmail)
}

// postImagePath is the on-disk path for a post's image JPEG. Post ids aren't
// sensitive (unlike e-mails), so the id is the filename directly.
func postImagePath(id int64) string {
	return filepath.Join(cfg.PostImageDir, strconv.FormatInt(id, 10)+".jpg")
}

// fitWithin downsizes src to fit inside a max×max box, preserving aspect ratio,
// and never upscales. Like squareThumb it's a plain nearest-neighbour sampler —
// good enough for forum images and keeps the zero-dependency rule.
func fitWithin(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= max && h <= max) {
		return src // already small enough (or degenerate): re-encode as-is
	}
	dw, dh := w, h
	if w >= h {
		dw, dh = max, h*max/w
	} else {
		dw, dh = w*max/h, max
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := b.Min.Y + y*h/dh
		for x := 0; x < dw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/dw, sy))
		}
	}
	return dst
}

// canPost reports whether email may create posts/comments and returns the
// account's profile (for the author handle). Posting is FREE — anyone with an
// account needs only a chosen username, since every post carries a handle like
// Twitter (a paid membership buys guaranteed critique, not the right to post).
// The one-post-per-day limit (enforced in insertPost) is the only throttle.
// Returns errNoUsername so the UI can explain why.
func canPost(email string) (profile, error) {
	p, err := getProfile(email)
	if err != nil {
		return profile{}, err
	}
	if p.Username == "" {
		return p, errNoUsername
	}
	return p, nil
}

// writeCanPostErr maps a canPost sentinel to an HTTP status + message.
func writeCanPostErr(w http.ResponseWriter, err error) {
	switch err {
	case errNoUsername:
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		log.Printf("canPost: %v", err)
		http.Error(w, "erro ao verificar acesso", http.StatusBadGateway)
	}
}

// avatarVer is the cache-busting value the frontend appends to avatar URLs.
func avatarVer(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

// postJSON shapes a feed row for the browser: the author as a small
// {username, has_avatar, avatar_ver} object (matching header.js), the timestamp
// as RFC3339, and — like every public response — no e-mail.
func postJSON(p forumPost) map[string]any {
	return map[string]any{
		"id":            p.ID,
		"board":         p.Board,
		"body":          p.Body,
		"has_image":     p.HasImage,
		"created_at":    p.CreatedAt.UTC().Format(time.RFC3339),
		"comment_count": p.CommentCount,
		"like_count":    p.LikeCount,
		"liked":         false, // overwritten per caller by markLiked
		"author": map[string]any{
			"username":   p.AuthorUser,
			"has_avatar": p.AuthorAvatar,
			"avatar_ver": avatarVer(p.AuthorAvAt),
			"member":     p.AuthorMember,
		},
	}
}

// commentJSON shapes a comment row like postJSON.
func commentJSON(c forumComment) map[string]any {
	m := map[string]any{
		"id":         c.ID,
		"post_id":    c.PostID,
		"body":       c.Body,
		"created_at": c.CreatedAt.UTC().Format(time.RFC3339),
		"like_count": c.LikeCount,
		"liked":      false, // overwritten per caller by markLiked
		"author": map[string]any{
			"username":   c.AuthorUser,
			"has_avatar": c.AuthorAvatar,
			"avatar_ver": avatarVer(c.AuthorAvAt),
		},
	}
	if c.ParentCommentID != nil {
		m["parent_comment_id"] = *c.ParentCommentID
		m["reply_to_username"] = c.ReplyToUsername
	}
	return m
}

// forumBoards is the set of boards posts may live in: "feedback" (share work for
// critique) and "sketchbooks" (sketchbook pages). validBoard keeps a posted value
// inside the set, defaulting to "feedback".
var forumBoards = map[string]bool{"feedback": true, "sketchbooks": true}

func validBoard(b string) string {
	b = strings.ToLower(strings.TrimSpace(b))
	if forumBoards[b] {
		return b
	}
	return "feedback"
}

// splitFirstLine peels the post's first non-blank line off as its title (the UI
// shows it slightly larger), stripping a leading markdown "#". The remainder is
// the body. Mirrors the split the community page does client-side.
func splitFirstLine(body string) (title, rest string) {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return "", ""
	}
	title = strings.TrimSpace(strings.TrimLeft(lines[i], "# "))
	rest = strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
	return title, rest
}

// searchPosts returns posts whose body matches q (case-insensitive, anywhere),
// newest first. The query's PostgREST filter metacharacters are neutralised first.
func searchPosts(q string, limit int) ([]forumPost, error) {
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		switch r {
		case '*', '%', ',', '(', ')':
			return ' '
		}
		return r
	}, q))
	if cleaned == "" {
		return nil, nil
	}
	return restSelect[forumPost]("search posts",
		"/rest/v1/forum_feed?select=*&body=ilike."+url.QueryEscape("*"+cleaned+"*")+
			"&order=id.desc&limit="+strconv.Itoa(limit))
}

// searchResultJSON shapes a post for the unified search list: a title, a short
// plain-text snippet, and just enough to link to it. No e-mail (the view omits it).
func searchResultJSON(p forumPost) map[string]any {
	title, rest := splitFirstLine(p.Body)
	snippet := rest
	if r := []rune(snippet); len(r) > 140 {
		snippet = strings.TrimSpace(string(r[:140])) + "…"
	}
	return map[string]any{
		"id":         p.ID,
		"board":      p.Board,
		"title":      title,
		"snippet":    snippet,
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339),
		"author":     map[string]any{"username": p.AuthorUser},
	}
}

// handleSearchAPI powers the forum half of the unified search on the lessons page
// (lessons first, then these posts). Public; needs at least 2 characters.
func handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := make([]map[string]any, 0)
	if utf8.RuneCountInString(q) >= 2 {
		posts, err := searchPosts(q, 10)
		if err != nil {
			log.Printf("searchPosts: %v", err)
			http.Error(w, "erro na busca", http.StatusBadGateway)
			return
		}
		for _, p := range posts {
			out = append(out, searchResultJSON(p))
		}
	}
	writeJSON(w, map[string]any{"posts": out})
}

// getFeed returns up to limit posts newest-first from the public forum_feed view.
// When before > 0 only posts older than that id are returned (keyset paging).
//
// membersOnly is the owner's review queue: only posts by paying members, oldest
// first (the longest-waiting work to critique comes up first), and unpaginated —
// the queue is small, so before is ignored and limit caps the whole list.
func getFeed(board string, before int64, limit int, membersOnly bool) ([]forumPost, error) {
	q := "/rest/v1/forum_feed?select=*&limit=" + strconv.Itoa(limit)
	if board != "" {
		q += "&board=eq." + url.QueryEscape(board)
	}
	if membersOnly {
		q += "&author_is_member=is.true&order=id.asc"
	} else {
		q += "&order=id.desc"
		if before > 0 {
			q += "&id=lt." + strconv.FormatInt(before, 10)
		}
	}
	return restSelect[forumPost]("get feed", q)
}

// getPost returns a single post from the view, or a zero post (ID == 0) if none.
func getPost(id int64) (forumPost, error) {
	rows, err := restSelect[forumPost]("get post",
		"/rest/v1/forum_feed?select=*&id=eq."+strconv.FormatInt(id, 10)+"&limit=1")
	if err != nil || len(rows) == 0 {
		return forumPost{}, err
	}
	return rows[0], nil
}

// getComments returns a post's comments oldest-first from the public view.
func getComments(postID int64) ([]forumComment, error) {
	return restSelect[forumComment]("get comments",
		"/rest/v1/forum_comments_view?select=*&post_id=eq."+
			strconv.FormatInt(postID, 10)+"&order=id.asc")
}

// errPostedToday signals that the member has hit the daily post limit
// (dailyPostLimit). Returned by insertPost; deleting one of today's posts frees a
// slot so they can post again.
var errPostedToday = fmt.Errorf("você atingiu o limite de %d publicações por dia; apague uma publicação de hoje ou volte amanhã", dailyPostLimit)

// brazilZone is the calendar the daily post limit lives in. Brazil dropped
// daylight saving in 2019, so a fixed -03:00 is exact.
var brazilZone = time.FixedZone("BRT", -3*60*60)

// postsToday counts email's live posts today (Brazil time) — the daily limit is
// now a count (dailyPostLimit), so the page can show whether a slot is left
// before the member types instead of after they submit.
func postsToday(email string) (int, error) {
	now := time.Now().In(brazilZone)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, brazilZone)
	rows, err := restSelect[struct {
		ID int64 `json:"id"`
	}]("posts today", "/rest/v1/forum_posts?select=id&author_email=eq."+
		url.QueryEscape(strings.ToLower(email))+"&created_at=gte."+
		url.QueryEscape(start.UTC().Format(time.RFC3339)))
	return len(rows), err
}

// handlePostedToday answers {"posted_today": bool} — true once the account is at
// the daily limit — so the compose box can swap itself for an explanation up front.
func handlePostedToday(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	n, err := postsToday(email)
	if err != nil {
		log.Printf("postsToday: %v", err)
		http.Error(w, "erro ao verificar", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"posted_today": n >= dailyPostLimit})
}

// insertPost creates a post and returns its new id. has_image starts false; the
// caller flips it once the image file is written (so the filename can use the id).
// The daily limit (dailyPostLimit) is enforced here by counting today's posts;
// the old uq_forum_posts_author_day index (1/day) is dropped by migration, but
// its violation is still mapped to errPostedToday in case it lingers.
func insertPost(authorEmail, board, body string) (int64, error) {
	if n, err := postsToday(authorEmail); err != nil {
		return 0, err
	} else if n >= dailyPostLimit {
		return 0, errPostedToday
	}
	b, err := restWrite("insert post", "POST", "/rest/v1/forum_posts",
		map[string]any{"author_email": strings.ToLower(authorEmail), "board": board, "body": body},
		"return=representation")
	if err != nil {
		if strings.Contains(err.Error(), "uq_forum_posts_author_day") {
			return 0, errPostedToday
		}
		return 0, err
	}
	var rows []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("insert post: no row returned")
	}
	return rows[0].ID, nil
}

// setPostHasImage flags that a post's image file is now on disk.
func setPostHasImage(id int64) error {
	_, err := restWrite("flag post image", "PATCH",
		"/rest/v1/forum_posts?id=eq."+strconv.FormatInt(id, 10),
		map[string]any{"has_image": true}, "")
	return err
}

// insertComment adds a comment to a post. parentCommentID, when non-nil, makes it
// a reply to an existing comment. A foreign-key violation (the post or parent was
// deleted) surfaces as a normal error the handler reports.
func insertComment(postID int64, authorEmail, body string, parentCommentID *int64) error {
	row := map[string]any{"post_id": postID, "author_email": strings.ToLower(authorEmail), "body": body}
	if parentCommentID != nil {
		row["parent_comment_id"] = *parentCommentID
	}
	_, err := restWrite("insert comment", "POST", "/rest/v1/forum_comments", row, "")
	return err
}

// getPostOwner returns a post's author e-mail + has_image flag, read from the base
// table (the feed view hides the e-mail). found is false when no such post exists.
func getPostOwner(id int64) (email string, hasImage, found bool, err error) {
	rows, err := restSelect[struct {
		Email    string `json:"author_email"`
		HasImage bool   `json:"has_image"`
	}]("get post owner", "/rest/v1/forum_posts?select=author_email,has_image&id=eq."+
		strconv.FormatInt(id, 10)+"&limit=1")
	if err != nil || len(rows) == 0 {
		return "", false, false, err
	}
	return rows[0].Email, rows[0].HasImage, true, nil
}

// getCommentOwner returns a comment's author e-mail from the base table. found is
// false when no such comment exists.
func getCommentOwner(id int64) (email string, found bool, err error) {
	rows, err := restSelect[struct {
		Email string `json:"author_email"`
	}]("get comment owner", "/rest/v1/forum_comments?select=author_email&id=eq."+
		strconv.FormatInt(id, 10)+"&limit=1")
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	return rows[0].Email, true, nil
}

// updatePostBody replaces a post's text. created_at is untouched, so the
// one-post-per-day unique index never re-fires on an edit.
func updatePostBody(id int64, body string) error {
	_, err := restWrite("update post", "PATCH",
		"/rest/v1/forum_posts?id=eq."+strconv.FormatInt(id, 10),
		map[string]any{"body": body}, "")
	return err
}

// updateCommentBody replaces a comment's text.
func updateCommentBody(id int64, body string) error {
	_, err := restWrite("update comment", "PATCH",
		"/rest/v1/forum_comments?id=eq."+strconv.FormatInt(id, 10),
		map[string]any{"body": body}, "")
	return err
}

// deletePost removes a post; its comments go with it via ON DELETE CASCADE.
func deletePost(id int64) error {
	_, err := restWrite("delete post", "DELETE",
		"/rest/v1/forum_posts?id=eq."+strconv.FormatInt(id, 10), nil, "")
	return err
}

// deleteComment removes a single comment.
func deleteComment(id int64) error {
	_, err := restWrite("delete comment", "DELETE",
		"/rest/v1/forum_comments?id=eq."+strconv.FormatInt(id, 10), nil, "")
	return err
}

// likedSet returns which of ids the account already liked, as a set. table/col
// name one of the two like tables. Errors are non-fatal by design — the page
// still renders, the hearts just come up empty — so they're logged here and a
// nil set is returned.
func likedSet(table, col, email string, ids []int64) map[int64]bool {
	if email == "" || len(ids) == 0 {
		return nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	rows, err := restSelect[map[string]int64]("liked set",
		"/rest/v1/"+table+"?select="+col+"&user_email=eq."+url.QueryEscape(strings.ToLower(email))+
			"&"+col+"=in.("+strings.Join(parts, ",")+")")
	if err != nil {
		log.Printf("likedSet %s: %v", table, err)
		return nil
	}
	set := make(map[int64]bool, len(rows))
	for _, r := range rows {
		set[r[col]] = true
	}
	return set
}

// markLiked flips the "liked" flag on already-shaped post/comment JSON rows
// whose id is in the caller's liked set.
func markLiked(rows []map[string]any, liked map[int64]bool) {
	for _, m := range rows {
		if id, ok := m["id"].(int64); ok && liked[id] {
			m["liked"] = true
		}
	}
}

// handleLikeAPI sets or clears the caller's like on a post or a comment (POST;
// form: post_id XOR comment_id, plus on=1|0 — explicit, so a retried request
// can't accidentally toggle back). Any logged-in account may like — applause is
// free and needs no membership — but a session is required, so counts can't be
// inflated anonymously.
func handleLikeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	postID, _ := strconv.ParseInt(r.FormValue("post_id"), 10, 64)
	commentID, _ := strconv.ParseInt(r.FormValue("comment_id"), 10, 64)
	var table, col string
	var id int64
	switch {
	case postID > 0 && commentID == 0:
		table, col, id = "forum_post_likes", "post_id", postID
	case commentID > 0 && postID == 0:
		table, col, id = "forum_comment_likes", "comment_id", commentID
	default:
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if r.FormValue("on") == "1" {
		_, err := restWrite("like", "POST", "/rest/v1/"+table+"?on_conflict="+col+",user_email",
			map[string]any{col: id, "user_email": strings.ToLower(email)},
			"resolution=ignore-duplicates")
		if err != nil {
			if strings.Contains(err.Error(), "23503") { // FK violation: target was deleted
				http.Error(w, "a publicação não existe mais", http.StatusNotFound)
				return
			}
			log.Printf("like: %v", err)
			http.Error(w, "não foi possível curtir", http.StatusBadGateway)
			return
		}
	} else {
		if _, err := restWrite("unlike", "DELETE",
			"/rest/v1/"+table+"?"+col+"=eq."+strconv.FormatInt(id, 10)+
				"&user_email=eq."+url.QueryEscape(strings.ToLower(email)), nil, ""); err != nil {
			log.Printf("unlike: %v", err)
			http.Error(w, "não foi possível remover a curtida", http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostsAPI lists the feed (GET, public) or creates a post (POST, members).
func handlePostsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleFeedList(w, r)
	case http.MethodPost:
		handlePostCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFeedList returns a page of the newest posts as JSON. ?before=<id> pages
// back through older posts for "load more". It asks the view for one row beyond
// the page so has_more is exact — the UI only offers "load more" when a click
// will actually land posts (a count that's a multiple of the page size used to
// produce one final empty click).
func handleFeedList(w http.ResponseWriter, r *http.Request) {
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		before, _ = strconv.ParseInt(v, 10, 64)
	}
	board := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("board")))
	// The owner's "members first" review queue: only the owner can request it,
	// and it's a single unpaginated page (oldest member posts first).
	membersOnly := r.URL.Query().Get("members") == "1" && isOwner(currentEmail(r))
	limit := feedPageSize + 1
	if membersOnly {
		limit = 100
	}
	posts, err := getFeed(board, before, limit, membersOnly)
	if err != nil {
		log.Printf("getFeed: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	hasMore := !membersOnly && len(posts) > feedPageSize
	if hasMore {
		posts = posts[:feedPageSize]
	}
	out := make([]map[string]any, 0, len(posts))
	ids := make([]int64, 0, len(posts))
	for _, p := range posts {
		out = append(out, postJSON(p))
		ids = append(ids, p.ID)
	}
	markLiked(out, likedSet("forum_post_likes", "post_id", currentEmail(r), ids))
	writeJSON(w, map[string]any{"posts": out, "has_more": hasMore})
}

// handlePostCreate creates a post for the logged-in member. Multipart fields:
//
//	body  - text (required unless an image is attached; capped at maxPostChars)
//	image - optional picture (PNG/JPEG/GIF), downsized to a small JPEG on disk
func handlePostCreate(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := canPost(email); err != nil {
		writeCanPostErr(w, err)
		return
	}
	// Cap the upload so a huge file can't exhaust memory while decoding.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	body := strings.TrimSpace(r.FormValue("body"))
	if utf8.RuneCountInString(body) > maxPostChars {
		http.Error(w, "mensagem muito longa", http.StatusBadRequest)
		return
	}
	// Decode + downsize the image (if any) BEFORE inserting, so a bad image is
	// rejected without leaving a text row behind.
	var imgBuf *bytes.Buffer
	if file, _, err := r.FormFile("image"); err == nil {
		defer file.Close()
		src, _, err := image.Decode(file)
		if err != nil {
			http.Error(w, "imagem inválida (use PNG, JPEG ou GIF)", http.StatusBadRequest)
			return
		}
		imgBuf = &bytes.Buffer{}
		if err := jpeg.Encode(imgBuf, fitWithin(src, postImageMax), &jpeg.Options{Quality: 80}); err != nil {
			log.Printf("post image encode: %v", err)
			http.Error(w, "não foi possível processar a imagem", http.StatusInternalServerError)
			return
		}
	}
	if body == "" && imgBuf == nil {
		http.Error(w, "escreva algo ou anexe uma imagem", http.StatusBadRequest)
		return
	}
	id, err := insertPost(email, validBoard(r.FormValue("board")), body)
	if err == errPostedToday {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	if err != nil {
		log.Printf("insertPost: %v", err)
		http.Error(w, "não foi possível publicar", http.StatusBadGateway)
		return
	}
	if imgBuf != nil {
		if err := os.WriteFile(postImagePath(id), imgBuf.Bytes(), 0o644); err != nil {
			log.Printf("post image write: %v", err)
			_ = deletePost(id) // roll back so the feed doesn't show a broken image
			http.Error(w, "não foi possível salvar a imagem", http.StatusInternalServerError)
			return
		}
		if err := setPostHasImage(id); err != nil {
			log.Printf("post image flag: %v", err)
			_ = os.Remove(postImagePath(id))
			_ = deletePost(id)
			http.Error(w, "não foi possível publicar", http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostAPI returns one post plus its comments (GET, public).
func handlePostAPI(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	post, err := getPost(id)
	if err != nil {
		log.Printf("getPost: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if post.ID == 0 {
		http.Error(w, "publicação não encontrada", http.StatusNotFound)
		return
	}
	comments, err := getComments(id)
	if err != nil {
		log.Printf("getComments: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	cs := make([]map[string]any, 0, len(comments))
	cids := make([]int64, 0, len(comments))
	for _, c := range comments {
		cs = append(cs, commentJSON(c))
		cids = append(cids, c.ID)
	}
	pj := postJSON(post)
	if email := currentEmail(r); email != "" {
		markLiked([]map[string]any{pj}, likedSet("forum_post_likes", "post_id", email, []int64{post.ID}))
		markLiked(cs, likedSet("forum_comment_likes", "comment_id", email, cids))
	}
	writeJSON(w, map[string]any{"post": pj, "comments": cs})
}

// handleCommentsAPI lists a post's comments (GET, public) or adds one (POST,
// members). Splitting reads out of /api/post lets the feed expand a thread
// without also refetching the post and its comment-count subquery.
func handleCommentsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleCommentsList(w, r)
	case http.MethodPost:
		handleCommentCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCommentsList returns a post's comments as JSON (public).
func handleCommentsList(w http.ResponseWriter, r *http.Request) {
	postID, _ := strconv.ParseInt(r.URL.Query().Get("post_id"), 10, 64)
	if postID <= 0 {
		http.Error(w, "publicação inválida", http.StatusBadRequest)
		return
	}
	comments, err := getComments(postID)
	if err != nil {
		log.Printf("getComments: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	cs := make([]map[string]any, 0, len(comments))
	cids := make([]int64, 0, len(comments))
	for _, c := range comments {
		cs = append(cs, commentJSON(c))
		cids = append(cids, c.ID)
	}
	markLiked(cs, likedSet("forum_comment_likes", "comment_id", currentEmail(r), cids))
	writeJSON(w, map[string]any{"comments": cs})
}

// handleCommentCreate adds a comment to a post (members only; reached via
// handleCommentsAPI on POST). Optional parent_comment_id makes it a reply.
func handleCommentCreate(w http.ResponseWriter, r *http.Request) {
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := canPost(email); err != nil {
		writeCanPostErr(w, err)
		return
	}
	postID, _ := strconv.ParseInt(r.FormValue("post_id"), 10, 64)
	if postID <= 0 {
		http.Error(w, "publicação inválida", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "escreva um comentário", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(body) > maxCommentChars {
		http.Error(w, "comentário muito longo", http.StatusBadRequest)
		return
	}
	var parentCommentID *int64
	if pidStr := r.FormValue("parent_comment_id"); pidStr != "" {
		pid, err := strconv.ParseInt(pidStr, 10, 64)
		if err != nil || pid <= 0 {
			http.Error(w, "comentário pai inválido", http.StatusBadRequest)
			return
		}
		// verify the parent belongs to the same post
		rows, err := restSelect[struct {
			PostID int64 `json:"post_id"`
		}]("check parent comment", "/rest/v1/forum_comments?select=post_id&id=eq."+
			strconv.FormatInt(pid, 10)+"&limit=1")
		if err != nil || len(rows) == 0 || rows[0].PostID != postID {
			http.Error(w, "comentário pai inválido", http.StatusBadRequest)
			return
		}
		parentCommentID = &pid
	}
	if err := insertComment(postID, email, body, parentCommentID); err != nil {
		log.Printf("insertComment: %v", err)
		http.Error(w, "não foi possível comentar", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostDelete removes a post (cascading its comments) when the caller is the
// author or the forum owner; best-effort removes its on-disk image too.
func handlePostDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	owner, hasImage, found, err := getPostOwner(id)
	if err != nil {
		log.Printf("getPostOwner: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent) // already gone — treat as success
		return
	}
	if !strings.EqualFold(owner, email) && !isOwner(email) {
		http.Error(w, "sem permissão", http.StatusForbidden)
		return
	}
	if err := deletePost(id); err != nil {
		log.Printf("deletePost: %v", err)
		http.Error(w, "não foi possível apagar", http.StatusBadGateway)
		return
	}
	if hasImage {
		_ = os.Remove(postImagePath(id))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostEdit lets a post's AUTHOR fix its text (form fields: id, body).
// Author-only — the owner moderates by deleting, never by rewriting someone
// else's words. The image, if any, stays as it is.
func handlePostEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if utf8.RuneCountInString(body) > maxPostChars {
		http.Error(w, "mensagem muito longa", http.StatusBadRequest)
		return
	}
	owner, hasImage, found, err := getPostOwner(id)
	if err != nil {
		log.Printf("getPostOwner: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if !found {
		http.Error(w, "publicação não encontrada", http.StatusNotFound)
		return
	}
	if !strings.EqualFold(owner, email) {
		http.Error(w, "sem permissão", http.StatusForbidden)
		return
	}
	if body == "" && !hasImage {
		http.Error(w, "escreva algo — esta publicação não tem imagem", http.StatusBadRequest)
		return
	}
	if err := updatePostBody(id, body); err != nil {
		log.Printf("updatePostBody: %v", err)
		http.Error(w, "não foi possível salvar", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCommentEdit lets a comment's author fix its text (form fields: id, body).
func handleCommentEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "escreva um comentário", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(body) > maxCommentChars {
		http.Error(w, "comentário muito longo", http.StatusBadRequest)
		return
	}
	owner, found, err := getCommentOwner(id)
	if err != nil {
		log.Printf("getCommentOwner: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if !found {
		http.Error(w, "comentário não encontrado", http.StatusNotFound)
		return
	}
	if !strings.EqualFold(owner, email) {
		http.Error(w, "sem permissão", http.StatusForbidden)
		return
	}
	if err := updateCommentBody(id, body); err != nil {
		log.Printf("updateCommentBody: %v", err)
		http.Error(w, "não foi possível salvar", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCommentDelete removes a comment when the caller is its author or the owner.
func handleCommentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := currentEmail(r)
	if email == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	owner, found, err := getCommentOwner(id)
	if err != nil {
		log.Printf("getCommentOwner: %v", err)
		http.Error(w, "erro ao carregar", http.StatusBadGateway)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !strings.EqualFold(owner, email) && !isOwner(email) {
		http.Error(w, "sem permissão", http.StatusForbidden)
		return
	}
	if err := deleteComment(id); err != nil {
		log.Printf("deleteComment: %v", err)
		http.Error(w, "não foi possível apagar", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostImage serves a post's image JPEG (/post-img/<id>). Public + cacheable;
// a post's image never changes, so no cache-busting is needed. 404 when none.
func handlePostImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/post-img/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	path := postImagePath(id)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// ---------------------------------------------------------------------------

func main() {
	cfg = loadConfig()

	if err := os.MkdirAll(cfg.AvatarDir, 0o755); err != nil {
		log.Fatalf("could not create avatar dir %q: %v", cfg.AvatarDir, err)
	}
	if err := os.MkdirAll(cfg.PostImageDir, 0o755); err != nil {
		log.Fatalf("could not create post image dir %q: %v", cfg.PostImageDir, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic)
	mux.HandleFunc("/lessons.html", handleLessons)
	mux.HandleFunc("/sitemap.xml", handleSitemap)
	mux.HandleFunc("/robots.txt", handleRobots)
	mux.HandleFunc("/login", handleLoginPage)
	mux.HandleFunc("/profile", page("web/profile.html"))
	mux.HandleFunc("/signup", handleSignupPage)
	mux.HandleFunc("/forgot", page("web/forgot.html"))
	mux.HandleFunc("/reset", page("web/reset.html"))
	mux.HandleFunc("/check-email", page("web/check-email.html"))
	mux.HandleFunc("/after-login", handleAfterLogin)
	mux.HandleFunc("/auth/login", handleSignin)
	mux.HandleFunc("/auth/signup", handleSignup)
	mux.HandleFunc("/auth/resend", handleResend)
	mux.HandleFunc("/auth/recover", handleRecover)
	mux.HandleFunc("/auth/reset", handleResetPassword)
	// Link-based flows (email confirmation, the anon buyer's one-time login
	// link, password recovery) land on /auth/callback with a GoTrue token in
	// the URL fragment (#...), which the browser does NOT send to the server.
	// callback.html reads the fragment: recovery links are routed to /reset;
	// everything else POSTs the access_token to /auth/session.
	mux.HandleFunc("/auth/callback", page("web/callback.html"))
	mux.HandleFunc("/auth/session", handleSession)
	mux.HandleFunc("/auth/logout", handleLogout)
	mux.HandleFunc("/me", handleMe)
	mux.HandleFunc("/api/profile", handleProfileAPI)
	mux.HandleFunc("/api/profile/username", handleUsername)
	mux.HandleFunc("/api/profile/bio", handleBio)
	mux.HandleFunc("/api/avatar", handleAvatarUpload)
	mux.HandleFunc("/avatar/me", handleAvatarMe)
	mux.HandleFunc("/avatar/u/", handlePublicAvatar)
	mux.HandleFunc("/api/u", handlePublicProfileAPI)
	mux.HandleFunc("/api/heatmap", handleHeatmapAPI)
	mux.HandleFunc("/api/complete", handleCompleteLesson)
	mux.HandleFunc("/api/completed", handleCompletedAPI)
	// The community feed shell is public; its compose/reply/delete controls
	// gate themselves off /me and the write endpoints re-check membership.
	mux.HandleFunc("/community", page("web/community.html"))
	// A single post on its own page (/post/<id>), Twitter-style. Same shell as
	// the community feed; the page reads the id from the path and shows just the
	// post + its comments. (Distinct from /post-img/<id>, the image route.)
	mux.HandleFunc("/post/", page("web/community.html"))
	mux.HandleFunc("/api/posts", handlePostsAPI)
	mux.HandleFunc("/api/posts/delete", handlePostDelete)
	mux.HandleFunc("/api/posts/edit", handlePostEdit)
	mux.HandleFunc("/api/posts/today", handlePostedToday)
	mux.HandleFunc("/api/post", handlePostAPI)
	mux.HandleFunc("/api/comments", handleCommentsAPI)
	mux.HandleFunc("/api/comments/delete", handleCommentDelete)
	mux.HandleFunc("/api/comments/edit", handleCommentEdit)
	mux.HandleFunc("/api/like", handleLikeAPI)
	mux.HandleFunc("/post-img/", handlePostImage)
	mux.HandleFunc("/api/search", handleSearchAPI)
	mux.HandleFunc("/header.js", script("web/header.js"))
	mux.HandleFunc("/join.js", script("web/join.js"))
	mux.HandleFunc("/comprar", handleBuy)
	mux.HandleFunc("/navy", handleBuy) // friendlier alias for the checkout page
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
