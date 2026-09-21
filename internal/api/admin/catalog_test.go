package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

func TestGenericAdminBindCannotMassAssignInternalFields(t *testing.T) {
	ctx := context.Background()
	e, st, _ := credentialTestAPI(t)
	attackerUUID := "11111111-1111-1111-1111-111111111111"
	oldTime := "1999-01-01T00:00:00Z"
	createBody := `{"id":"` + attackerUUID + `","name":"Mass Provider","slug":"mass-provider","base_url":"https://example.com","enabled":true,"created_at":"` + oldTime + `","updated_at":"` + oldTime + `"}`
	createResp := adminRequest(t, e, http.MethodPost, "/api/providers", createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d: %s", createResp.Code, createResp.Body.String())
	}
	var created models.Provider
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.UUID == attackerUUID {
		t.Fatal("create response used request-supplied public id")
	}
	if created.CreatedAt.Format(time.RFC3339) == oldTime {
		t.Fatal("create response used request-supplied created_at")
	}

	updateBody := `{"id":"` + attackerUUID + `","name":"Mass Provider Updated","slug":"mass-provider","base_url":"https://example.com","enabled":true,"created_at":"` + oldTime + `"}`
	updateResp := adminRequest(t, e, http.MethodPut, "/api/providers/"+created.UUID, updateBody)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected update status 200, got %d: %s", updateResp.Code, updateResp.Body.String())
	}
	var updated models.Provider
	if err := json.Unmarshal(updateResp.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.UUID != created.UUID {
		t.Fatalf("expected update to preserve public id %q, got %q", created.UUID, updated.UUID)
	}

	var stored models.Provider
	if err := st.FindByUUID(ctx, &stored, created.UUID); err != nil {
		t.Fatal(err)
	}
	if stored.CreatedAt.Format(time.RFC3339) == oldTime {
		t.Fatal("stored provider used request-supplied created_at")
	}
	if stored.Slug != "mass-provider-updated" {
		t.Fatalf("stored provider slug = %q, want server-derived mass-provider-updated", stored.Slug)
	}
}

func TestCatalogNameConflictsReturnActionableConflict(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	first := adminRequest(t, e, http.MethodPost, "/api/routing-lanes", `{"name":"API Conflict Group","enabled":true}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first group create status 201, got %d: %s", first.Code, first.Body.String())
	}
	conflict := adminRequest(t, e, http.MethodPost, "/api/routing-lanes", `{"name":"api conflict GROUP","enabled":true}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected case-insensitive group conflict status 409, got %d: %s", conflict.Code, conflict.Body.String())
	}
	if !strings.Contains(conflict.Body.String(), store.ErrRoutingLaneNameConflict.Error()) {
		t.Fatalf("expected actionable group conflict response, got %s", conflict.Body.String())
	}
}

func TestProviderDeleteUsesSoftCatalogModeWhenEnabled(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	secrets, err := security.NewLocalSecretManager(adminTestMasterKey(), st)
	if err != nil {
		t.Fatal(err)
	}
	provider := models.Provider{Name: "Pro Soft Delete", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		Name: "Pro Soft Model", RouteKind: models.RouteKindChat, Enabled: true,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	api := New(st, nil, nil, secrets, adminTestToken(), SystemInfo{}).
		WithSoftDeleteProviderCatalog(true)
	e := echo.New()
	api.Register(e)
	response := adminRequest(t, e, http.MethodDelete, "/api/providers/"+provider.UUID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d: %s", response.Code, response.Body.String())
	}

	var storedProvider models.Provider
	if err := st.DB().Unscoped().First(&storedProvider, provider.ID).Error; err != nil {
		t.Fatal(err)
	}
	var storedEndpoint models.Endpoint
	if err := st.DB().Unscoped().First(&storedEndpoint, endpoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !storedProvider.DeletedAt.Valid || !storedEndpoint.DeletedAt.Valid {
		t.Fatalf("expected retained soft-deleted rows, provider=%#v endpoint=%#v", storedProvider, storedEndpoint)
	}
}

func TestAdminCatalogViewsApplyExternalQueryScopes(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).
		WithExternalQueryScopeHooks(func(_ *echo.Context, input QueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error) {
			switch input.Resource {
			case "providers":
				return []func(*gorm.DB) *gorm.DB{func(db *gorm.DB) *gorm.DB {
					return db.Where("providers.slug <> ?", "hidden-provider")
				}}, nil
			case "endpoints":
				return []func(*gorm.DB) *gorm.DB{func(db *gorm.DB) *gorm.DB {
					return db.Where("endpoints.slug <> ?", "hidden-model")
				}}, nil
			default:
				return nil, nil
			}
		})
	e := echo.New()
	api.Register(e)

	visibleProvider := models.Provider{Name: "Visible Provider", Slug: "visible-provider", BaseURL: "https://visible.example", Enabled: true}
	hiddenProvider := models.Provider{Name: "Hidden Provider", Slug: "hidden-provider", BaseURL: "https://hidden.example", Enabled: true}
	for _, provider := range []*models.Provider{&visibleProvider, &hiddenProvider} {
		if err := st.Create(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}
	hiddenEndpoint := models.Endpoint{
		Name: "Hidden Model", Slug: "hidden-model", ProviderID: hiddenProvider.ID,
		ProviderUUID: hiddenProvider.UUID, UpstreamModel: "hidden", Enabled: true,
	}
	if err := st.Create(ctx, &hiddenEndpoint); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/providers", "/api/page-data/providers"} {
		response := adminRequest(t, e, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		if !strings.Contains(body, "visible-provider") || strings.Contains(body, "hidden-provider") ||
			strings.Contains(body, "hidden-model") {
			t.Fatalf("%s ignored catalog scopes: %s", path, body)
		}
	}
}
