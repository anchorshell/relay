package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/security"
)

func TestCredentialAdminAPIDoesNotPersistOrReturnPlaintext(t *testing.T) {
	ctx := context.Background()
	e, st, secrets := credentialTestAPI(t)
	provider := models.Provider{Name: "OpenAI", Slug: "openai-admin", BaseURL: "https://api.openai.com/v1", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}

	createBody := `{"provider_id":"` + provider.UUID + `","name":"primary","secret":"sk-admin-create-sentinel","enabled":true}`
	createResp := adminRequest(t, e, http.MethodPost, "/api/credentials", createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d: %s", createResp.Code, createResp.Body.String())
	}
	assertNoCredentialLeak(t, createResp.Body.String(), "sk-admin-create-sentinel")

	var created CredentialResponse
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.HasSecret || created.ID == "" || created.ProviderID != provider.UUID {
		t.Fatalf("unexpected credential response: %#v", created)
	}

	var stored models.Credential
	if err := st.FindByUUID(ctx, &stored, created.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.EncryptedSecret, "sk-admin-create-sentinel") {
		t.Fatal("database stored plaintext credential secret")
	}
	if !strings.HasPrefix(stored.EncryptedSecret, "relay:v1:aes-256-gcm:local:") {
		t.Fatalf("expected encrypted envelope in DB, got %q", stored.EncryptedSecret)
	}
	secret, err := secrets.SecretForCredential(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-admin-create-sentinel" {
		t.Fatalf("expected cached created secret, got %q", secret)
	}

	listResp := adminRequest(t, e, http.MethodGet, "/api/credentials", "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d: %s", listResp.Code, listResp.Body.String())
	}
	assertNoCredentialLeak(t, listResp.Body.String(), "sk-admin-create-sentinel")

	pageDataResp := adminRequest(t, e, http.MethodGet, "/api/page-data/setup", "")
	if pageDataResp.Code != http.StatusOK {
		t.Fatalf("expected page data status 200, got %d: %s", pageDataResp.Code, pageDataResp.Body.String())
	}
	assertNoCredentialLeak(t, pageDataResp.Body.String(), "sk-admin-create-sentinel")
}

func TestCredentialAdminAPIUpdatesAndEvictsCache(t *testing.T) {
	ctx := context.Background()
	e, st, secrets := credentialTestAPI(t)
	provider := models.Provider{Name: "OpenAI", Slug: "openai-admin-update", BaseURL: "https://api.openai.com/v1", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}

	createResp := adminRequest(t, e, http.MethodPost, "/api/credentials", `{"provider_id":"`+provider.UUID+`","name":"primary","secret":"sk-admin-original","enabled":true}`)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d: %s", createResp.Code, createResp.Body.String())
	}
	var created CredentialResponse
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var stored models.Credential
	if err := st.FindByUUID(ctx, &stored, created.ID); err != nil {
		t.Fatal(err)
	}

	updateResp := adminRequest(t, e, http.MethodPut, "/api/credentials/"+created.ID, `{"provider_id":"`+provider.UUID+`","name":"primary","secret":"sk-admin-updated","enabled":true}`)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected update status 200, got %d: %s", updateResp.Code, updateResp.Body.String())
	}
	assertNoCredentialLeak(t, updateResp.Body.String(), "sk-admin-updated")
	if err := st.FindByUUID(ctx, &stored, created.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.EncryptedSecret, "sk-admin-updated") || strings.Contains(stored.EncryptedSecret, "sk-admin-original") {
		t.Fatal("database stored plaintext credential secret after update")
	}
	secret, err := secrets.SecretForCredential(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-admin-updated" {
		t.Fatalf("expected updated cached secret, got %q", secret)
	}

	disableResp := adminRequest(t, e, http.MethodPut, "/api/credentials/"+created.ID, `{"provider_id":"`+provider.UUID+`","name":"primary","enabled":false}`)
	if disableResp.Code != http.StatusOK {
		t.Fatalf("expected disable status 200, got %d: %s", disableResp.Code, disableResp.Body.String())
	}
	if _, err := secrets.SecretForCredential(ctx, stored.ID); !errors.Is(err, security.ErrSecretNotFound) {
		t.Fatalf("expected disabled credential to be evicted, got %v", err)
	}

	enableResp := adminRequest(t, e, http.MethodPut, "/api/credentials/"+created.ID, `{"provider_id":"`+provider.UUID+`","name":"primary","enabled":true}`)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("expected enable status 200, got %d: %s", enableResp.Code, enableResp.Body.String())
	}
	secret, err = secrets.SecretForCredential(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "sk-admin-updated" {
		t.Fatalf("expected re-enabled credential to restore cache, got %q", secret)
	}

	deleteResp := adminRequest(t, e, http.MethodDelete, "/api/credentials/"+created.ID, "")
	if deleteResp.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d: %s", deleteResp.Code, deleteResp.Body.String())
	}
	if _, err := secrets.SecretForCredential(ctx, stored.ID); !errors.Is(err, security.ErrSecretNotFound) {
		t.Fatalf("expected deleted credential to be evicted, got %v", err)
	}
}
