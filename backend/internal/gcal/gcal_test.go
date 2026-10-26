package gcal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"testing"
)

func testConfig(key string) Config {
	return Config{ClientID: "fixture-client", ClientSecret: "fixture-secret", TokenKey: key,
		RedirectURL: "https://class.example/api/auth/google/callback"}
}

func testKey(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

// Без любого из трёх ключей интеграция выключена, а не сломана: api и
// worker поднимаются. Битый ключ — ошибка старта, как регион у storage.
func TestNewWithoutKeysIsDisabled(t *testing.T) {
	for _, cfg := range []Config{{}, {ClientID: "id", ClientSecret: "secret"}, {TokenKey: testKey(1)}} {
		c, err := New(cfg)
		if c != nil || err != nil {
			t.Fatalf("New(%+v) = %v, %v; want disabled", cfg, c, err)
		}
	}
	if got := (Config{ClientID: "id"}).Missing(); !slices.Equal(got, []string{"GOOGLE_CLIENT_SECRET", "GOOGLE_TOKEN_KEY"}) {
		t.Fatalf("Missing = %v", got)
	}
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	for _, key := range []string{"not base64!", short} {
		if _, err := New(testConfig(key)); !errors.Is(err, ErrBadTokenKey) {
			t.Fatalf("key %q: err = %v, want ErrBadTokenKey", key, err)
		}
	}
}

// Шифртекст не содержит токена, каждый раз свой и открывается только тем же
// ключом и для той же строки users.
func TestSealOpen(t *testing.T) {
	c, err := New(testConfig(testKey(1)))
	if err != nil || c == nil {
		t.Fatalf("New = %v, %v", c, err)
	}
	const token = "1//refresh-token-fixture"
	a, err := c.Seal(7, token)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Seal(7, token)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(a, []byte(token)) || bytes.Equal(a, b) {
		t.Fatal("ciphertext leaks the token or repeats the nonce")
	}
	if got, err := c.Open(7, a); err != nil || got != token {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := c.Open(8, a); err == nil {
		t.Fatal("ciphertext moved to another user must not open")
	}
	other, _ := New(testConfig(testKey(2)))
	if _, err := other.Open(7, a); err == nil {
		t.Fatal("another key must not open the token")
	}
	tampered := bytes.Clone(a)
	tampered[len(tampered)-1] ^= 1
	for _, bad := range [][]byte{tampered, a[:5], append([]byte{2}, a[1:]...)} {
		if _, err := c.Open(7, bad); err == nil {
			t.Fatalf("corrupted ciphertext opened: %x", bad)
		}
	}
}

// Экран согласия: offline-доступ, повторное согласие, только scope событий,
// state и PKCE S256 от выданного verifier.
func TestAuthCodeURL(t *testing.T) {
	c, _ := New(testConfig(testKey(1)))
	raw, verifier := c.AuthCodeURL("fixture-state")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "fixture-client",
		"redirect_uri":          "https://class.example/api/auth/google/callback",
		"scope":                 Scope,
		"access_type":           "offline",
		"prompt":                "consent",
		"state":                 "fixture-state",
		"code_challenge_method": "S256",
		"code_challenge":        base64.RawURLEncoding.EncodeToString(sum[:]),
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if u.Scheme+"://"+u.Host+u.Path != defaultAuthURL || len(verifier) < 43 {
		t.Fatalf("auth URL %s, verifier %q", raw, verifier)
	}
	if _, again := c.AuthCodeURL("fixture-state"); again == verifier {
		t.Fatal("verifier must be fresh for every start")
	}
}
