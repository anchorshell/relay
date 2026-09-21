package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type adminPageDataResponse struct {
	Page       string                    `json:"page"`
	Catalog    *adminCatalogPageData     `json:"catalog,omitempty"`
	QueueItems *[]scheduler.TaskSnapshot `json:"queue_items,omitempty"`
	Settings   *[]models.AppSetting      `json:"settings,omitempty"`
	System     *SystemInfo               `json:"system,omitempty"`
}

type adminCatalogPageData struct {
	Providers         *[]models.Provider         `json:"providers,omitempty"`
	Credentials       *[]CredentialResponse      `json:"credentials,omitempty"`
	Endpoints         *[]models.Endpoint         `json:"endpoints,omitempty"`
	Lanes             *[]models.RoutingLane      `json:"lanes,omitempty"`
	Memberships       *[]models.LaneMembership   `json:"memberships,omitempty"`
	LimitPolicies     *[]models.LimitPolicy      `json:"limit_policies,omitempty"`
	ObservedLimits    *[]models.ObservedLimit    `json:"observed_limits,omitempty"`
	PricingPolicies   *[]models.PricingPolicy    `json:"pricing_policies,omitempty"`
	Guardrails        *[]guardrailResponse       `json:"guardrails,omitempty"`
	GuardrailBindings *[]models.GuardrailBinding `json:"guardrail_bindings,omitempty"`
}

type adminCatalogNeeds struct {
	providers       bool
	credentials     bool
	endpoints       bool
	lanes           bool
	memberships     bool
	limitPolicies   bool
	observedLimits  bool
	pricingPolicies bool
	guardrails      bool
}

func (a *API) pageData(c *echo.Context) error {
	page := strings.ToLower(strings.TrimSpace(c.Param("page")))
	ctx := c.Request().Context()
	resp := adminPageDataResponse{Page: page}

	switch page {
	case "dashboard":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{
			providers: true, endpoints: true, lanes: true, memberships: true,
			limitPolicies: true, observedLimits: true, guardrails: true,
		})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
		if err := a.attachQueueItems(c, &resp); err != nil {
			return err
		}
	case "providers":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{
			providers: true, credentials: true, endpoints: true,
			limitPolicies: true, pricingPolicies: true, guardrails: true,
		})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
	case "groups":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{providers: true, endpoints: true, lanes: true, memberships: true, guardrails: true})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
	case "limits":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{providers: true, endpoints: true, limitPolicies: true, observedLimits: true})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
	case "usage":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{providers: true, endpoints: true, lanes: true})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
		if err := a.attachQueueItems(c, &resp); err != nil {
			return err
		}
	case "requests", "logs":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{providers: true, endpoints: true, lanes: true})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
		if err := a.attachSettings(ctx, &resp); err != nil {
			return err
		}
	case "queue":
		if err := a.attachQueueItems(c, &resp); err != nil {
			return err
		}
	case "playground":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{providers: true, endpoints: true, lanes: true, memberships: true})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
	case "realtime":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{
			providers: true, endpoints: true, lanes: true, memberships: true,
			limitPolicies: true, observedLimits: true, guardrails: true,
		})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
		if err := a.attachQueueItems(c, &resp); err != nil {
			return err
		}
	case "settings":
		if err := a.attachSettings(ctx, &resp); err != nil {
			return err
		}
		system := a.systemInfo
		resp.System = &system
	case "setup":
		catalog, err := a.catalogPageData(c, adminCatalogNeeds{
			providers: true, credentials: true, endpoints: true, lanes: true, memberships: true,
			limitPolicies: true, observedLimits: true, pricingPolicies: true, guardrails: true,
		})
		if err != nil {
			return err
		}
		resp.Catalog = catalog
	default:
		return echo.NewHTTPError(http.StatusNotFound, "unknown page data bundle")
	}

	return c.JSON(http.StatusOK, resp)
}

func (a *API) attachQueueItems(c *echo.Context, resp *adminPageDataResponse) error {
	items, err := a.scopedQueueItems(c)
	if err != nil {
		return err
	}
	resp.QueueItems = &items
	return nil
}

func (a *API) attachSettings(ctx context.Context, resp *adminPageDataResponse) error {
	settings, err := a.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	resp.Settings = &settings
	return nil
}

func (a *API) catalogPageData(c *echo.Context, needs adminCatalogNeeds) (*adminCatalogPageData, error) {
	ctx := c.Request().Context()
	data := &adminCatalogPageData{}
	resourceScopes := func(resource string) ([]func(*gorm.DB) *gorm.DB, error) {
		return a.queryScopes(c, resource)
	}
	providerScopes, err := resourceScopes("providers")
	if err != nil {
		return nil, err
	}
	endpointScopes, err := resourceScopes("endpoints")
	if err != nil {
		return nil, err
	}
	var decoratedEndpoints []models.Endpoint
	endpointsLoaded := false

	loadDecoratedEndpoints := func() ([]models.Endpoint, error) {
		if endpointsLoaded {
			return decoratedEndpoints, nil
		}
		items, err := a.store.ListEndpointsWithScopes(ctx, endpointScopes...)
		if err != nil {
			return nil, err
		}
		items, err = a.decorateEndpointHealth(ctx, items)
		if err != nil {
			return nil, err
		}
		decoratedEndpoints = items
		endpointsLoaded = true
		return decoratedEndpoints, nil
	}

	if needs.providers {
		providers, err := a.store.ListProvidersWithScopes(ctx, providerScopes...)
		if err != nil {
			return nil, err
		}
		endpoints, err := loadDecoratedEndpoints()
		if err != nil {
			return nil, err
		}
		deriveProviderHealth(providers, endpoints)
		data.Providers = &providers
	}

	if needs.credentials {
		scopes, err := resourceScopes("credentials")
		if err != nil {
			return nil, err
		}
		credentials, err := a.store.ListCredentialsWithScopes(ctx, scopes...)
		if err != nil {
			return nil, err
		}
		responses := credentialResponses(credentials)
		data.Credentials = &responses
	}

	if needs.endpoints {
		endpoints, err := loadDecoratedEndpoints()
		if err != nil {
			return nil, err
		}
		data.Endpoints = &endpoints
	}

	if needs.lanes {
		lanes, err := a.store.ListLanes(ctx)
		if err != nil {
			return nil, err
		}
		data.Lanes = &lanes
	}

	if needs.memberships {
		scopes, err := resourceScopes("lane_memberships")
		if err != nil {
			return nil, err
		}
		memberships, err := a.store.ListLaneMembershipsWithScopes(ctx, scopes...)
		if err != nil {
			return nil, err
		}
		data.Memberships = &memberships
	}

	if needs.limitPolicies {
		scopes, err := resourceScopes("limit_policies")
		if err != nil {
			return nil, err
		}
		policies, err := a.store.ListLimitPoliciesWithScopes(ctx, scopes...)
		if err != nil {
			return nil, err
		}
		data.LimitPolicies = &policies
	}

	if needs.observedLimits {
		scopes, err := resourceScopes("observed_limits")
		if err != nil {
			return nil, err
		}
		observed, err := a.store.ListObservedLimitsWithScopes(ctx, scopes...)
		if err != nil {
			return nil, err
		}
		data.ObservedLimits = &observed
	}

	if needs.pricingPolicies {
		scopes, err := resourceScopes("pricing_policies")
		if err != nil {
			return nil, err
		}
		pricing, err := a.store.ListPricingPoliciesWithScopes(ctx, scopes...)
		if err != nil {
			return nil, err
		}
		data.PricingPolicies = &pricing
	}

	if needs.guardrails {
		guardrails, err := a.store.ListGuardrails(ctx)
		if err != nil {
			return nil, err
		}
		bindings, err := a.store.ListGuardrailBindings(ctx, 0)
		if err != nil {
			return nil, err
		}
		counts := make(map[uint]int, len(guardrails))
		for _, binding := range bindings {
			counts[binding.GuardrailID]++
		}
		responses := make([]guardrailResponse, 0, len(guardrails))
		for _, guardrail := range guardrails {
			summary := models.Guardrail{UUID: guardrail.UUID, Name: guardrail.Name, Enabled: guardrail.Enabled, PresetSlug: guardrail.PresetSlug, PreDispatchEnabled: guardrail.PreDispatchEnabled, PostResponseEnabled: guardrail.PostResponseEnabled}
			responses = append(responses, guardrailResponse{Guardrail: summary, HasCredential: guardrail.CredentialID != nil, BindingCount: counts[guardrail.ID]})
		}
		data.Guardrails = &responses
		data.GuardrailBindings = &bindings
	}

	return data, nil
}
