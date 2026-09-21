package security

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestAESGCMEncryptDecryptRoundTrip(t *testing.T) {
	manager := testManager(t, nil)
	plaintext := []byte("sk-test-provider-secret")

	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(envelope, "relay:v1:aes-256-gcm:local:") {
		t.Fatalf("expected relay envelope, got %q", envelope)
	}

	decrypted, err := manager.DecryptCredential(context.Background(), 12, 34, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("expected round trip plaintext %q, got %q", plaintext, decrypted)
	}
}

func TestDecryptFailsWhenCiphertextTampered(t *testing.T) {
	manager := testManager(t, nil)
	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, []byte("sk-test-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.DecryptCredential(context.Background(), 12, 34, tamperEnvelopePart(t, envelope, 5))
	if err == nil {
		t.Fatal("expected tampered ciphertext to fail decryption")
	}
}

func TestDecryptFailsWhenNonceTampered(t *testing.T) {
	manager := testManager(t, nil)
	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, []byte("sk-test-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.DecryptCredential(context.Background(), 12, 34, tamperEnvelopePart(t, envelope, 4))
	if err == nil {
		t.Fatal("expected tampered nonce to fail decryption")
	}
}

func TestDecryptFailsWhenAADChanges(t *testing.T) {
	manager := testManager(t, nil)
	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, []byte("sk-test-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.DecryptCredential(context.Background(), 13, 34, envelope); err == nil {
		t.Fatal("expected changed credential id AAD to fail decryption")
	}
	if _, err := manager.DecryptCredential(context.Background(), 12, 35, envelope); err == nil {
		t.Fatal("expected changed provider id AAD to fail decryption")
	}
}

func TestUnknownEnvelopeVersionRejected(t *testing.T) {
	manager := testManager(t, nil)
	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, []byte("sk-test-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ":")
	parts[1] = "v2"

	if _, err := manager.DecryptCredential(context.Background(), 12, 34, strings.Join(parts, ":")); err == nil {
		t.Fatal("expected unknown envelope version to be rejected")
	}
}

func TestUnknownEnvelopeAlgorithmRejected(t *testing.T) {
	manager := testManager(t, nil)
	envelope, err := manager.EncryptCredential(context.Background(), 12, 34, []byte("sk-test-provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ":")
	parts[2] = "aes-256-cbc"

	if _, err := manager.DecryptCredential(context.Background(), 12, 34, strings.Join(parts, ":")); err == nil {
		t.Fatal("expected unknown envelope algorithm to be rejected")
	}
}

func TestMalformedEnvelopeRejected(t *testing.T) {
	manager := testManager(t, nil)
	malformed := []string{
		"",
		"relay:v1:aes-256-gcm",
		"relay:v1:aes-256-gcm:local:not-base64:not-base64",
		"relay:v1:aes-256-gcm:local:short:ciphertext",
	}
	for _, envelope := range malformed {
		if _, err := manager.DecryptCredential(context.Background(), 12, 34, envelope); err == nil {
			t.Fatalf("expected malformed envelope %q to be rejected", envelope)
		}
	}
}

func TestMasterKeyMustDecodeToExactly32Bytes(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := DecodeMasterKey(shortKey); err == nil {
		t.Fatal("expected short master key to be rejected")
	}
}

func TestMasterKeyChangeMeRejected(t *testing.T) {
	if _, err := DecodeMasterKey("change-me"); err == nil {
		t.Fatal("expected placeholder master key to be rejected")
	}
}

func TestCredentialCacheLoadedAtStartup(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "OpenAI", Slug: "openai", BaseURL: "https://api.openai.com/v1", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{ProviderID: provider.ID, Name: "primary", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, st)
	envelope, err := manager.EncryptCredential(ctx, credential.ID, credential.ProviderID, []byte("sk-cache-load-sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCredentialEncryptedSecret(ctx, credential.ID, envelope); err != nil {
		t.Fatal(err)
	}

	if err := manager.LoadCredentialCache(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := manager.SecretForCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-cache-load-sentinel" {
		t.Fatalf("expected cached secret, got %q", secret)
	}
	secret[0] = 'X'
	again, err := manager.SecretForCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != "sk-cache-load-sentinel" {
		t.Fatalf("expected cache to return defensive copies, got %q", again)
	}
}

func TestCredentialCacheIsOrganizationScoped(t *testing.T) {
	const (
		orgA = "11111111-1111-4111-8111-111111111111"
		orgB = "22222222-2222-4222-8222-222222222222"
	)
	st := &credentialCacheEntryStore{}
	manager := testManager(t, st)

	envelopeA, err := manager.EncryptCredential(context.Background(), 7, 70, []byte("sk-org-a"))
	if err != nil {
		t.Fatal(err)
	}
	envelopeB, err := manager.EncryptCredential(context.Background(), 7, 70, []byte("sk-org-b"))
	if err != nil {
		t.Fatal(err)
	}
	st.entries = []models.CredentialCacheEntry{
		{
			Credential:       models.Credential{ID: 7, ProviderID: 70, EncryptedSecret: envelopeA, Enabled: true},
			OrganizationUUID: orgA,
		},
		{
			Credential:       models.Credential{ID: 7, ProviderID: 70, EncryptedSecret: envelopeB, Enabled: true},
			OrganizationUUID: orgB,
		},
	}

	if err := manager.LoadCredentialCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	secretA, err := manager.SecretForCredential(testTenantContext(orgA), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secretA) != "sk-org-a" {
		t.Fatalf("expected org A secret, got %q", secretA)
	}
	secretB, err := manager.SecretForCredential(testTenantContext(orgB), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secretB) != "sk-org-b" {
		t.Fatalf("expected org B secret, got %q", secretB)
	}
	if _, err := manager.SecretForCredential(context.Background(), 7); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected unscoped credential lookup to miss, got %v", err)
	}

	manager.UpsertCachedCredential(testTenantContext(orgA), 7, []byte("sk-org-a-rotated"))
	secretA, err = manager.SecretForCredential(testTenantContext(orgA), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secretA) != "sk-org-a-rotated" {
		t.Fatalf("expected org A rotated secret, got %q", secretA)
	}
	secretB, err = manager.SecretForCredential(testTenantContext(orgB), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secretB) != "sk-org-b" {
		t.Fatalf("expected org B secret to be unchanged, got %q", secretB)
	}

	manager.ForgetCredential(testTenantContext(orgA), 7)
	if _, err := manager.SecretForCredential(testTenantContext(orgA), 7); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected org A credential to be forgotten, got %v", err)
	}
	secretB, err = manager.SecretForCredential(testTenantContext(orgB), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secretB) != "sk-org-b" {
		t.Fatalf("expected org B secret after org A delete, got %q", secretB)
	}
}

func TestSecretForCredentialLazyLoadsThenReadsMemory(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "OpenAI", Slug: "openai-hot", BaseURL: "https://api.openai.com/v1", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{ProviderID: provider.ID, Name: "primary", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, st)
	envelope, err := manager.EncryptCredential(ctx, credential.ID, credential.ProviderID, []byte("sk-hot-path-sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCredentialEncryptedSecret(ctx, credential.ID, envelope); err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadCredentialCache(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := manager.SecretForCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-hot-path-sentinel" {
		t.Fatalf("expected lazy-loaded cache value, got %q", secret)
	}
	if err := st.UpdateCredentialEncryptedSecret(ctx, credential.ID, "relay:v1:aes-256-gcm:local:bad:bad"); err != nil {
		t.Fatal(err)
	}

	secret, err = manager.SecretForCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-hot-path-sentinel" {
		t.Fatalf("expected hot path cache value, got %q", secret)
	}
}

func TestLegacyPlaintextCredentialIsEncryptedDuringCacheLoad(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "OpenAI", Slug: "openai-legacy", BaseURL: "https://api.openai.com/v1", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{
		ProviderID:      provider.ID,
		Name:            "legacy",
		EncryptedSecret: "sk-legacy-plaintext-sentinel",
		Enabled:         true,
	}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, st)

	if err := manager.LoadCredentialCache(ctx); err != nil {
		t.Fatal(err)
	}
	var stored models.Credential
	if err := st.FindByID(ctx, &stored, credential.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.EncryptedSecret, "sk-legacy-plaintext-sentinel") {
		t.Fatal("expected legacy plaintext to be replaced with encrypted envelope")
	}
	if !strings.HasPrefix(stored.EncryptedSecret, "relay:v1:aes-256-gcm:local:") {
		t.Fatalf("expected encrypted envelope in DB, got %q", stored.EncryptedSecret)
	}
}

func tamperEnvelopePart(t *testing.T, envelope string, part int) string {
	t.Helper()
	parts := strings.Split(envelope, ":")
	if len(parts) <= part {
		t.Fatalf("envelope missing part %d: %q", part, envelope)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[part])
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) == 0 {
		t.Fatal("cannot tamper empty envelope part")
	}
	decoded[0] ^= 0xff
	parts[part] = base64.RawURLEncoding.EncodeToString(decoded)
	return strings.Join(parts, ":")
}

func testManager(t *testing.T, st CredentialStore) *LocalSecretManager {
	t.Helper()
	manager, err := NewLocalSecretManager(testMasterKey(), st)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestCredentialTenantEvictionZerosSecretBytes(t *testing.T) {
	manager := testManager(t, &credentialCacheEntryStore{})
	ctx := testTenantContext("org-secret")
	manager.UpsertCachedCredential(ctx, 9, []byte("top-secret"))
	key := credentialCacheKey{OrganizationUUID: "org-secret", CredentialID: 9}
	manager.mu.Lock()
	retained := manager.cache[key].secret
	manager.mu.Unlock()
	manager.EvictTenant("org-secret")
	for index, value := range retained {
		if value != 0 {
			t.Fatalf("evicted secret byte %d was not zeroed", index)
		}
	}
}

func testMasterKey() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestGuardrailCredentialUsesEncryptedDomainSeparatedEnvelope(t *testing.T) {
	store := &guardrailCredentialTestStore{credential: models.GuardrailCredential{ID: 7, Enabled: true}}
	manager := testManager(t, store)
	envelope, err := manager.EncryptGuardrailCredential(context.Background(), 7, []byte("guardrail-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(envelope, "guardrail-secret") {
		t.Fatal("plaintext was retained in the envelope")
	}
	store.credential.EncryptedSecret = envelope
	secret, err := manager.GuardrailSecret(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "guardrail-secret" {
		t.Fatalf("unexpected secret %q", secret)
	}
	if _, err := manager.DecryptCredential(context.Background(), 7, 1, envelope); err == nil {
		t.Fatal("guardrail envelope decrypted in the provider credential domain")
	}
}

type guardrailCredentialTestStore struct{ credential models.GuardrailCredential }

func (s *guardrailCredentialTestStore) ListCredentials(context.Context) ([]models.Credential, error) {
	return nil, nil
}
func (s *guardrailCredentialTestStore) UpdateCredentialEncryptedSecret(context.Context, uint, string) error {
	return nil
}
func (s *guardrailCredentialTestStore) GetGuardrailCredential(_ context.Context, id uint) (models.GuardrailCredential, error) {
	if id != s.credential.ID {
		return models.GuardrailCredential{}, ErrSecretNotFound
	}
	return s.credential, nil
}
func (s *guardrailCredentialTestStore) UpdateGuardrailCredentialEnvelope(_ context.Context, id uint, envelope string) error {
	if id != s.credential.ID {
		return ErrSecretNotFound
	}
	s.credential.EncryptedSecret = envelope
	return nil
}

func testTenantContext(orgUUID string) context.Context {
	return tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: orgUUID})
}

type credentialCacheEntryStore struct {
	entries []models.CredentialCacheEntry
}

func (s *credentialCacheEntryStore) ListCredentials(ctx context.Context) ([]models.Credential, error) {
	credentials := make([]models.Credential, 0, len(s.entries))
	for _, entry := range s.entries {
		credentials = append(credentials, entry.Credential)
	}
	return credentials, nil
}

func (s *credentialCacheEntryStore) ListCredentialCacheEntries(ctx context.Context) ([]models.CredentialCacheEntry, error) {
	return append([]models.CredentialCacheEntry(nil), s.entries...), nil
}

func (s *credentialCacheEntryStore) UpdateCredentialEncryptedSecret(ctx context.Context, credentialID uint, envelope string) error {
	for i := range s.entries {
		if s.entries[i].Credential.ID == credentialID {
			s.entries[i].Credential.EncryptedSecret = envelope
		}
	}
	return nil
}
