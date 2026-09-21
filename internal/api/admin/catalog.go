package admin

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
	"github.com/labstack/echo/v5"
)

func (a *API) listProviders(c *echo.Context) error {
	providerScopes, err := a.queryScopes(c, "providers")
	if err != nil {
		return err
	}
	endpointScopes, err := a.queryScopes(c, "endpoints")
	if err != nil {
		return err
	}
	items, err := a.store.ListProvidersWithScopes(c.Request().Context(), providerScopes...)
	if err != nil {
		return err
	}
	endpoints, err := a.store.ListEndpointsWithScopes(c.Request().Context(), endpointScopes...)
	if err != nil {
		return err
	}
	endpoints, err = a.decorateEndpointHealth(c.Request().Context(), endpoints)
	if err != nil {
		return err
	}
	byProvider := make(map[uint]models.HealthStatus)
	for _, endpoint := range endpoints {
		current := byProvider[endpoint.ProviderID]
		if healthSeverity(endpoint.HealthStatus) > healthSeverity(current) {
			byProvider[endpoint.ProviderID] = endpoint.HealthStatus
		}
	}
	for i := range items {
		if status, ok := byProvider[items[i].ID]; ok {
			items[i].HealthStatus = status
			continue
		}
		if items[i].HealthStatus != models.HealthUnhealthy {
			items[i].HealthStatus = models.HealthHealthy
		}
	}
	return c.JSON(http.StatusOK, items)
}

func (a *API) deleteProvider(c *echo.Context) error {
	var provider models.Provider
	if err := a.store.FindByUUID(c.Request().Context(), &provider, c.Param("id")); err != nil {
		return err
	}
	credentials, err := a.store.ListCredentialsByProvider(c.Request().Context(), provider.ID)
	if err != nil {
		return err
	}
	deleteProvider := a.store.DeleteProviderCascade
	if a.softDeleteProviderCatalog {
		deleteProvider = a.store.SoftDeleteProviderCascade
	}
	if err := deleteProvider(c.Request().Context(), provider.ID); err != nil {
		return err
	}
	for _, credential := range credentials {
		a.secrets.ForgetCredential(c.Request().Context(), credential.ID)
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) deleteEndpoint(c *echo.Context) error {
	var endpoint models.Endpoint
	if err := a.store.FindByUUID(c.Request().Context(), &endpoint, c.Param("id")); err != nil {
		return err
	}
	deleteEndpoint := a.store.DeleteEndpointCascade
	if a.softDeleteProviderCatalog {
		deleteEndpoint = a.store.SoftDeleteEndpoint
	}
	if err := deleteEndpoint(c.Request().Context(), endpoint.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) deleteRoutingLane(c *echo.Context) error {
	var lane models.RoutingLane
	if err := a.store.FindByUUID(c.Request().Context(), &lane, c.Param("id")); err != nil {
		return err
	}
	if err := a.store.DeleteRoutingLaneCascade(c.Request().Context(), lane.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listEndpoints(c *echo.Context) error {
	scopes, err := a.queryScopes(c, "endpoints")
	if err != nil {
		return err
	}
	items, err := a.store.ListEndpointsWithScopes(c.Request().Context(), scopes...)
	if err != nil {
		return err
	}
	items, err = a.decorateEndpointHealth(c.Request().Context(), items)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

func (a *API) rankSuggestions(c *echo.Context) error {
	var endpoints []models.Endpoint
	if err := a.store.DB().WithContext(c.Request().Context()).Order("suggested_rank asc, suggested_score desc").Find(&endpoints).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, endpoints)
}

func (a *API) recomputeSuggestedRank(c *echo.Context) error {
	endpointID, err := a.store.EndpointIDByUUID(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	endpoint, err := a.store.RecomputeSuggestedRank(c.Request().Context(), endpointID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, endpoint)
}

func (a *API) recomputeAllSuggestions(c *echo.Context) error {
	if err := a.store.RecomputeAllSuggestions(c.Request().Context()); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func list[T any](st *store.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var items []T
		if err := st.DB().WithContext(c.Request().Context()).Order("id asc").Find(&items).Error; err != nil {
			return err
		}
		return c.JSON(http.StatusOK, items)
	}
}

func bindCreate[T any](st *store.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var item T
		if err := c.Bind(&item); err != nil {
			return err
		}
		scrubCreateModelFields(&item)
		if err := st.Create(c.Request().Context(), &item); err != nil {
			return catalogMutationError(&item, err)
		}
		return c.JSON(http.StatusCreated, item)
	}
}

func bindUpdate[T any](st *store.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var item T
		if err := st.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
			return err
		}
		internalID := getUintModelField(&item, "ID")
		publicUUID := getStringModelField(&item, "UUID")
		createdAt := getTimeModelField(&item, "CreatedAt")
		updatedAt := getTimeModelField(&item, "UpdatedAt")
		encryptedSecret := getStringModelField(&item, "EncryptedSecret")
		if err := c.Bind(&item); err != nil {
			return err
		}
		scrubUpdateModelFields(&item, internalID, publicUUID, createdAt, updatedAt, encryptedSecret)
		if err := st.Save(c.Request().Context(), &item); err != nil {
			return catalogMutationError(&item, err)
		}
		return c.JSON(http.StatusOK, item)
	}
}

func catalogMutationError(target any, err error) error {
	switch {
	case errors.Is(err, store.ErrProviderNameConflict),
		errors.Is(err, store.ErrEndpointNameConflict),
		errors.Is(err, store.ErrRoutingLaneNameConflict),
		errors.Is(err, store.ErrGuardrailNameConflict):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique constraint") &&
		!strings.Contains(message, "constraint failed") &&
		!strings.Contains(message, "duplicate key") {
		return err
	}
	switch target.(type) {
	case *models.Provider:
		return echo.NewHTTPError(http.StatusConflict, store.ErrProviderNameConflict.Error())
	case *models.Endpoint:
		return echo.NewHTTPError(http.StatusConflict, store.ErrEndpointNameConflict.Error())
	case *models.RoutingLane:
		return echo.NewHTTPError(http.StatusConflict, store.ErrRoutingLaneNameConflict.Error())
	case *models.Guardrail:
		return echo.NewHTTPError(http.StatusConflict, store.ErrGuardrailNameConflict.Error())
	default:
		return err
	}
}

func setUintModelField(target any, fieldName string, value uint) {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Uint {
		return
	}
	field.SetUint(uint64(value))
}

func getUintModelField(target any, fieldName string) uint {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return 0
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return 0
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.Uint {
		return 0
	}
	return uint(field.Uint())
}

func scrubCreateModelFields(target any) {
	setUintModelField(target, "ID", 0)
	setStringModelField(target, "UUID", "")
	setStringModelField(target, "EncryptedSecret", "")
	setTimeModelField(target, "CreatedAt", time.Time{})
	setTimeModelField(target, "UpdatedAt", time.Time{})
}

func scrubUpdateModelFields(target any, id uint, uuid string, createdAt, updatedAt time.Time, encryptedSecret string) {
	setUintModelField(target, "ID", id)
	setStringModelField(target, "UUID", uuid)
	setStringModelField(target, "EncryptedSecret", encryptedSecret)
	setTimeModelField(target, "CreatedAt", createdAt)
	setTimeModelField(target, "UpdatedAt", updatedAt)
}

func getStringModelField(target any, fieldName string) string {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return ""
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return ""
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func setStringModelField(target any, fieldName string, value string) {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.String {
		return
	}
	field.SetString(value)
}

func getTimeModelField(target any, fieldName string) time.Time {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return time.Time{}
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return time.Time{}
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || field.Type() != reflect.TypeOf(time.Time{}) {
		return time.Time{}
	}
	value, _ := field.Interface().(time.Time)
	return value
}

func setTimeModelField(target any, fieldName string, value time.Time) {
	ref := reflect.ValueOf(target)
	if ref.Kind() != reflect.Ptr || ref.IsNil() {
		return
	}
	elem := ref.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() || field.Type() != reflect.TypeOf(time.Time{}) {
		return
	}
	field.Set(reflect.ValueOf(value))
}

func deleteByUUID[T any](st *store.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var item T
		if err := st.DeleteByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
