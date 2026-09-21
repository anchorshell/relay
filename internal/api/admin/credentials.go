package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/labstack/echo/v5"
)

type CredentialResponse struct {
	ID         string    `json:"id"`
	ProviderID string    `json:"provider_id"`
	Name       string    `json:"name"`
	HasSecret  bool      `json:"has_secret"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func credentialResponse(item models.Credential) CredentialResponse {
	return CredentialResponse{
		ID:         item.UUID,
		ProviderID: item.ProviderUUID,
		Name:       item.Name,
		HasSecret:  strings.TrimSpace(item.EncryptedSecret) != "",
		Enabled:    item.Enabled,
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
}

func credentialResponses(items []models.Credential) []CredentialResponse {
	responses := make([]CredentialResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, credentialResponse(item))
	}
	return responses
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func (a *API) listCredentials(c *echo.Context) error {
	scopes, err := a.queryScopes(c, "credentials")
	if err != nil {
		return err
	}
	items, err := a.store.ListCredentialsWithScopes(c.Request().Context(), scopes...)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, credentialResponses(items))
}

func (a *API) createCredential(c *echo.Context) error {
	var payload struct {
		ProviderUUID string `json:"provider_id"`
		Name         string `json:"name"`
		Secret       string `json:"secret"`
		Enabled      bool   `json:"enabled"`
	}
	if err := c.Bind(&payload); err != nil {
		return err
	}
	secret := []byte(payload.Secret)
	defer zeroBytes(secret)
	if strings.TrimSpace(payload.Secret) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "credential secret is required")
	}
	item := models.Credential{
		ProviderUUID: payload.ProviderUUID,
		Name:         payload.Name,
		Enabled:      payload.Enabled,
	}
	if err := a.store.Create(c.Request().Context(), &item); err != nil {
		return err
	}
	encrypted, err := a.secrets.EncryptCredential(c.Request().Context(), item.ID, item.ProviderID, secret)
	if err != nil {
		_ = a.store.DeleteByID(c.Request().Context(), &models.Credential{}, item.ID)
		return err
	}
	item.EncryptedSecret = encrypted
	if err := a.store.Save(c.Request().Context(), &item); err != nil {
		return err
	}
	if item.Enabled {
		a.secrets.UpsertCachedCredential(c.Request().Context(), item.ID, secret)
	} else {
		a.secrets.ForgetCredential(c.Request().Context(), item.ID)
	}
	return c.JSON(http.StatusCreated, credentialResponse(item))
}

func (a *API) updateCredential(c *echo.Context) error {
	var item models.Credential
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	var payload struct {
		ProviderUUID string `json:"provider_id"`
		Name         string `json:"name"`
		Secret       string `json:"secret"`
		Enabled      bool   `json:"enabled"`
	}
	if err := c.Bind(&payload); err != nil {
		return err
	}
	oldProviderID := item.ProviderID
	oldEnvelope := item.EncryptedSecret
	item.ProviderID = 0
	item.ProviderUUID = payload.ProviderUUID
	item.Name = payload.Name
	item.Enabled = payload.Enabled

	if err := a.store.PopulatePublicUUIDs(c.Request().Context(), &item); err != nil {
		return err
	}

	var plaintext []byte
	if payload.Secret != "" {
		plaintext = []byte(payload.Secret)
		encrypted, err := a.secrets.EncryptCredential(c.Request().Context(), item.ID, item.ProviderID, plaintext)
		if err != nil {
			zeroBytes(plaintext)
			return err
		}
		item.EncryptedSecret = encrypted
	} else if item.ProviderID != oldProviderID && strings.TrimSpace(oldEnvelope) != "" {
		decrypted, err := a.secrets.DecryptCredential(c.Request().Context(), item.ID, oldProviderID, oldEnvelope)
		if err != nil {
			return err
		}
		plaintext = decrypted
		encrypted, err := a.secrets.EncryptCredential(c.Request().Context(), item.ID, item.ProviderID, plaintext)
		if err != nil {
			zeroBytes(plaintext)
			return err
		}
		item.EncryptedSecret = encrypted
	}
	if err := a.store.Save(c.Request().Context(), &item); err != nil {
		zeroBytes(plaintext)
		return err
	}

	if item.Enabled {
		if plaintext == nil && strings.TrimSpace(item.EncryptedSecret) != "" {
			decrypted, err := a.secrets.DecryptCredential(c.Request().Context(), item.ID, item.ProviderID, item.EncryptedSecret)
			if err != nil {
				return err
			}
			plaintext = decrypted
		}
		if plaintext != nil {
			a.secrets.UpsertCachedCredential(c.Request().Context(), item.ID, plaintext)
		}
	} else {
		a.secrets.ForgetCredential(c.Request().Context(), item.ID)
	}
	zeroBytes(plaintext)
	return c.JSON(http.StatusOK, credentialResponse(item))
}

func (a *API) deleteCredential(c *echo.Context) error {
	var item models.Credential
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	if err := a.store.DeleteByID(c.Request().Context(), &models.Credential{}, item.ID); err != nil {
		return err
	}
	a.secrets.ForgetCredential(c.Request().Context(), item.ID)
	return c.NoContent(http.StatusNoContent)
}
