package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
)

const (
	envelopeProduct   = "relay"
	envelopeVersion   = "v1"
	envelopeAlgorithm = "aes-256-gcm"
	envelopeKeyID     = "local"
	nonceSize         = 12
)

var ErrSecretNotFound = errors.New("credential secret not found")

type SecretManager interface {
	EncryptCredential(ctx context.Context, credentialID uint, providerID uint, plaintext []byte) (string, error)
	DecryptCredential(ctx context.Context, credentialID uint, providerID uint, envelope string) ([]byte, error)
	LoadCredentialCache(ctx context.Context) error
	SecretForCredential(ctx context.Context, credentialID uint) ([]byte, error)
	UpsertCachedCredential(ctx context.Context, credentialID uint, secret []byte)
	ForgetCredential(ctx context.Context, credentialID uint)
	EvictTenant(organizationUUID string)
}

type CredentialStore interface {
	ListCredentials(ctx context.Context) ([]models.Credential, error)
	UpdateCredentialEncryptedSecret(ctx context.Context, credentialID uint, envelope string) error
}

type CredentialCacheEntryStore interface {
	ListCredentialCacheEntries(ctx context.Context) ([]models.CredentialCacheEntry, error)
}

type CredentialCacheLookupStore interface {
	GetCredentialCacheEntry(ctx context.Context, credentialID uint) (models.CredentialCacheEntry, error)
}

type CredentialCacheStreamStore interface {
	StreamCredentialCacheEntries(ctx context.Context, consume func(models.CredentialCacheEntry) error) error
}

type GuardrailCredentialStore interface {
	GetGuardrailCredential(ctx context.Context, credentialID uint) (models.GuardrailCredential, error)
	UpdateGuardrailCredentialEnvelope(ctx context.Context, credentialID uint, envelope string) error
}

type GuardrailSecretManager interface {
	EncryptGuardrailCredential(ctx context.Context, credentialID uint, plaintext []byte) (string, error)
	DecryptGuardrailCredential(ctx context.Context, credentialID uint, envelope string) ([]byte, error)
	GuardrailSecret(ctx context.Context, credentialID uint) ([]byte, error)
}

type credentialCacheKey struct {
	OrganizationUUID string
	CredentialID     uint
}

type LocalSecretManager struct {
	key   []byte
	store CredentialStore

	mu         sync.Mutex
	cache      map[credentialCacheKey]credentialSecretEntry
	cacheBytes int64
	maxEntries int
	maxBytes   int64
	cacheTTL   time.Duration
}

type credentialSecretEntry struct {
	secret     []byte
	lastAccess time.Time
}

const (
	credentialCacheMaxEntries = 10_000
	credentialCacheMaxBytes   = int64(8 << 20)
	credentialCacheTTL        = 15 * time.Minute
)

func NewLocalSecretManager(masterKey string, st CredentialStore) (*LocalSecretManager, error) {
	key, err := DecodeMasterKey(masterKey)
	if err != nil {
		return nil, err
	}
	return NewLocalSecretManagerFromKey(key, st)
}

func NewLocalSecretManagerFromKey(key []byte, st CredentialStore) (*LocalSecretManager, error) {
	if err := validateMasterKeyBytes(key); err != nil {
		return nil, err
	}
	return &LocalSecretManager{
		key:        cloneBytes(key),
		store:      st,
		cache:      make(map[credentialCacheKey]credentialSecretEntry),
		maxEntries: credentialPositiveIntEnv("ANCHORSHELL_CREDENTIAL_CACHE_MAX_ENTRIES", credentialCacheMaxEntries),
		maxBytes:   int64(credentialPositiveIntEnv("ANCHORSHELL_CREDENTIAL_CACHE_MAX_BYTES", int(credentialCacheMaxBytes))),
		cacheTTL:   credentialPositiveDurationEnv("ANCHORSHELL_CREDENTIAL_CACHE_IDLE_TTL", credentialCacheTTL),
	}, nil
}

func credentialPositiveIntEnv(name string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func credentialPositiveDurationEnv(name string, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func DecodeMasterKey(value string) ([]byte, error) {
	raw := strings.TrimSpace(value)
	if isPlaceholder(raw) {
		return nil, errors.New("RELAY_MASTER_KEY must be a base64-encoded 32-byte AES key")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("RELAY_MASTER_KEY must be valid base64")
	}
	if err := validateMasterKeyBytes(key); err != nil {
		return nil, err
	}
	return key, nil
}

func MasterKeyValid(value string) bool {
	_, err := DecodeMasterKey(value)
	return err == nil
}

func GenerateMasterKey() (string, error) {
	for {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return "", err
		}
		if validateMasterKeyBytes(key) == nil {
			return base64.StdEncoding.EncodeToString(key), nil
		}
	}
}

func IsCredentialEnvelope(value string) bool {
	return strings.HasPrefix(value, envelopeProduct+":")
}

func CredentialEnvelopePrefix() string {
	return envelopeProduct + ":"
}

func (m *LocalSecretManager) EncryptCredential(ctx context.Context, credentialID uint, providerID uint, plaintext []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if credentialID == 0 || providerID == 0 {
		return "", errors.New("credential encryption context is incomplete")
	}
	gcm, err := m.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, credentialAAD(credentialID, providerID))
	return strings.Join([]string{
		envelopeProduct,
		envelopeVersion,
		envelopeAlgorithm,
		envelopeKeyID,
		base64.RawURLEncoding.EncodeToString(nonce),
		base64.RawURLEncoding.EncodeToString(ciphertext),
	}, ":"), nil
}

func (m *LocalSecretManager) DecryptCredential(ctx context.Context, credentialID uint, providerID uint, envelope string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if credentialID == 0 || providerID == 0 {
		return nil, errors.New("credential decryption context is incomplete")
	}
	nonce, ciphertext, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	gcm, err := m.gcm()
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, credentialAAD(credentialID, providerID))
	if err != nil {
		return nil, errors.New("credential secret decrypt failed")
	}
	return plaintext, nil
}

func (m *LocalSecretManager) EncryptGuardrailCredential(ctx context.Context, credentialID uint, plaintext []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if credentialID == 0 {
		return "", errors.New("guardrail credential encryption context is incomplete")
	}
	gcm, err := m.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, guardrailCredentialAAD(credentialID))
	return strings.Join([]string{envelopeProduct, envelopeVersion, envelopeAlgorithm, envelopeKeyID, base64.RawURLEncoding.EncodeToString(nonce), base64.RawURLEncoding.EncodeToString(ciphertext)}, ":"), nil
}

func (m *LocalSecretManager) DecryptGuardrailCredential(ctx context.Context, credentialID uint, envelope string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if credentialID == 0 {
		return nil, errors.New("guardrail credential decryption context is incomplete")
	}
	nonce, ciphertext, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	gcm, err := m.gcm()
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, guardrailCredentialAAD(credentialID))
	if err != nil {
		return nil, errors.New("guardrail credential secret decrypt failed")
	}
	return plaintext, nil
}

func (m *LocalSecretManager) GuardrailSecret(ctx context.Context, credentialID uint) ([]byte, error) {
	store, ok := m.store.(GuardrailCredentialStore)
	if !ok {
		return nil, errors.New("guardrail credential store is unavailable")
	}
	credential, err := store.GetGuardrailCredential(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	if !credential.Enabled || strings.TrimSpace(credential.EncryptedSecret) == "" {
		return nil, ErrSecretNotFound
	}
	return m.DecryptGuardrailCredential(ctx, credential.ID, credential.EncryptedSecret)
}

func (m *LocalSecretManager) LoadCredentialCache(ctx context.Context) error {
	if m.store == nil {
		return errors.New("credential store is required")
	}
	// Tenant-aware stores support an indexed single-credential lookup. Leave
	// encrypted secrets out of the in-memory cache until first use. Stream the
	// rows once only to migrate any pre-envelope legacy values without ever
	// materializing the complete credential table.
	if _, lazy := m.store.(CredentialCacheLookupStore); lazy {
		streamer, ok := m.store.(CredentialCacheStreamStore)
		if !ok {
			return nil
		}
		return streamer.StreamCredentialCacheEntries(ctx, func(entry models.CredentialCacheEntry) error {
			credential := entry.Credential
			secret := strings.TrimSpace(credential.EncryptedSecret)
			if secret == "" || strings.HasPrefix(secret, envelopeProduct+":"+envelopeVersion+":"+envelopeAlgorithm+":") {
				return nil
			}
			plaintext, err := m.loadCredential(ctx, credential)
			zeroBytes(plaintext)
			if err != nil {
				return fmt.Errorf("migrate credential secret %d: %w", credential.ID, err)
			}
			return nil
		})
	}
	entries, err := m.credentialCacheEntries(ctx)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		credential := entry.Credential
		if strings.TrimSpace(credential.EncryptedSecret) == "" {
			continue
		}

		plaintext, err := m.loadCredential(ctx, credential)
		if err != nil {
			return fmt.Errorf("load credential secret %d: %w", credential.ID, err)
		}
		if _, lazy := m.store.(CredentialCacheLookupStore); !lazy && credential.Enabled {
			m.putCacheEntry(credentialCacheKey{OrganizationUUID: strings.TrimSpace(entry.OrganizationUUID), CredentialID: credential.ID}, plaintext)
		}
		zeroBytes(plaintext)
	}
	return nil
}

func (m *LocalSecretManager) credentialCacheEntries(ctx context.Context) ([]models.CredentialCacheEntry, error) {
	if provider, ok := m.store.(CredentialCacheEntryStore); ok {
		return provider.ListCredentialCacheEntries(ctx)
	}
	credentials, err := m.store.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	scope, _ := tenancy.ScopeFromContext(ctx)
	entries := make([]models.CredentialCacheEntry, 0, len(credentials))
	for _, credential := range credentials {
		entries = append(entries, models.CredentialCacheEntry{
			Credential:       credential,
			OrganizationUUID: scope.OrganizationUUID,
		})
	}
	return entries, nil
}

func (m *LocalSecretManager) SecretForCredential(ctx context.Context, credentialID uint) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := cacheKeyForContext(ctx, credentialID)
	m.mu.Lock()
	entry, ok := m.cache[key]
	if ok && time.Since(entry.lastAccess) <= m.cacheTTL {
		entry.lastAccess = time.Now().UTC()
		m.cache[key] = entry
		secret := cloneBytes(entry.secret)
		m.mu.Unlock()
		return secret, nil
	}
	if ok {
		m.deleteCacheEntryLocked(key)
	}
	m.mu.Unlock()
	provider, ok := m.store.(CredentialCacheLookupStore)
	if !ok {
		return nil, ErrSecretNotFound
	}
	stored, err := provider.GetCredentialCacheEntry(ctx, credentialID)
	if err != nil || !stored.Credential.Enabled || strings.TrimSpace(stored.Credential.EncryptedSecret) == "" {
		return nil, ErrSecretNotFound
	}
	secret, err := m.loadCredential(ctx, stored.Credential)
	if err != nil {
		return nil, err
	}
	m.putCacheEntry(key, secret)
	result := cloneBytes(secret)
	zeroBytes(secret)
	return result, nil
}

func (m *LocalSecretManager) UpsertCachedCredential(ctx context.Context, credentialID uint, secret []byte) {
	if credentialID == 0 {
		return
	}
	key := cacheKeyForContext(ctx, credentialID)
	next := cloneBytes(secret)
	m.mu.Lock()
	m.deleteCacheEntryLocked(key)
	m.cache[key] = credentialSecretEntry{secret: next, lastAccess: time.Now().UTC()}
	m.cacheBytes += int64(len(next))
	m.enforceCacheBoundsLocked()
	m.mu.Unlock()
}

func (m *LocalSecretManager) ForgetCredential(ctx context.Context, credentialID uint) {
	if credentialID == 0 {
		return
	}
	key := cacheKeyForContext(ctx, credentialID)
	m.mu.Lock()
	m.deleteCacheEntryLocked(key)
	m.mu.Unlock()
}

func (m *LocalSecretManager) putCacheEntry(key credentialCacheKey, secret []byte) {
	m.mu.Lock()
	m.deleteCacheEntryLocked(key)
	cloned := cloneBytes(secret)
	m.cache[key] = credentialSecretEntry{secret: cloned, lastAccess: time.Now().UTC()}
	m.cacheBytes += int64(len(cloned))
	m.enforceCacheBoundsLocked()
	m.mu.Unlock()
}

func (m *LocalSecretManager) deleteCacheEntryLocked(key credentialCacheKey) {
	entry, ok := m.cache[key]
	if !ok {
		return
	}
	delete(m.cache, key)
	m.cacheBytes -= int64(len(entry.secret))
	zeroBytes(entry.secret)
	if m.cacheBytes < 0 {
		m.cacheBytes = 0
	}
}

func (m *LocalSecretManager) enforceCacheBoundsLocked() {
	for len(m.cache) > m.maxEntries || m.cacheBytes > m.maxBytes {
		var oldestKey credentialCacheKey
		var oldest time.Time
		found := false
		for key, entry := range m.cache {
			if !found || entry.lastAccess.Before(oldest) {
				oldestKey, oldest, found = key, entry.lastAccess, true
			}
		}
		if !found {
			return
		}
		m.deleteCacheEntryLocked(oldestKey)
	}
}

func (m *LocalSecretManager) EvictTenant(organizationUUID string) {
	if m == nil {
		return
	}
	organizationUUID = strings.TrimSpace(organizationUUID)
	m.mu.Lock()
	for key := range m.cache {
		if key.OrganizationUUID == organizationUUID {
			m.deleteCacheEntryLocked(key)
		}
	}
	m.mu.Unlock()
}

func (m *LocalSecretManager) loadCredential(ctx context.Context, credential models.Credential) ([]byte, error) {
	if IsCredentialEnvelope(credential.EncryptedSecret) {
		return m.DecryptCredential(ctx, credential.ID, credential.ProviderID, credential.EncryptedSecret)
	}

	plaintext := []byte(credential.EncryptedSecret)
	envelope, err := m.EncryptCredential(ctx, credential.ID, credential.ProviderID, plaintext)
	if err != nil {
		zeroBytes(plaintext)
		return nil, err
	}
	if err := m.store.UpdateCredentialEncryptedSecret(ctx, credential.ID, envelope); err != nil {
		zeroBytes(plaintext)
		return nil, err
	}
	return plaintext, nil
}

func cacheKeyForContext(ctx context.Context, credentialID uint) credentialCacheKey {
	scope, _ := tenancy.ScopeFromContext(ctx)
	return credentialCacheKey{
		OrganizationUUID: strings.TrimSpace(scope.OrganizationUUID),
		CredentialID:     credentialID,
	}
}

func (m *LocalSecretManager) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(m.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() != nonceSize {
		return nil, errors.New("unexpected AES-GCM nonce size")
	}
	return gcm, nil
}

func parseEnvelope(envelope string) ([]byte, []byte, error) {
	parts := strings.Split(envelope, ":")
	if len(parts) != 6 {
		return nil, nil, errors.New("malformed credential secret envelope")
	}
	for _, part := range parts {
		if part == "" {
			return nil, nil, errors.New("malformed credential secret envelope")
		}
	}
	if parts[0] != envelopeProduct {
		return nil, nil, errors.New("malformed credential secret envelope")
	}
	if parts[1] != envelopeVersion {
		return nil, nil, errors.New("unknown credential secret envelope version")
	}
	if parts[2] != envelopeAlgorithm {
		return nil, nil, errors.New("unknown credential secret algorithm")
	}
	if parts[3] != envelopeKeyID {
		return nil, nil, errors.New("unknown credential secret key id")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, errors.New("invalid credential secret nonce encoding")
	}
	if len(nonce) != nonceSize {
		return nil, nil, errors.New("invalid credential secret nonce length")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, errors.New("invalid credential secret ciphertext encoding")
	}
	if len(ciphertext) == 0 {
		return nil, nil, errors.New("empty credential secret ciphertext")
	}
	return nonce, ciphertext, nil
}

func credentialAAD(credentialID uint, providerID uint) []byte {
	return []byte(fmt.Sprintf("relay credential secret v1\ncredential_id=%d\nprovider_id=%d", credentialID, providerID))
}

func guardrailCredentialAAD(credentialID uint) []byte {
	return []byte(fmt.Sprintf("relay guardrail credential secret v1\ncredential_id=%d", credentialID))
}

func validateMasterKeyBytes(key []byte) error {
	if len(key) != 32 {
		return errors.New("RELAY_MASTER_KEY must decode to exactly 32 bytes")
	}
	if allSameBytes(key) {
		return errors.New("RELAY_MASTER_KEY must not be all-zero or repeated bytes")
	}
	return nil
}

func isPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "change-me", "changeme", "placeholder", "secret", "password":
		return true
	default:
		return false
	}
}

func allSameBytes(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	first := value[0]
	for _, item := range value[1:] {
		if item != first {
			return false
		}
	}
	return true
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	copied := make([]byte, len(value))
	copy(copied, value)
	return copied
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func zeroSecretMap(values map[credentialCacheKey][]byte) {
	for key, value := range values {
		zeroBytes(value)
		delete(values, key)
	}
}
