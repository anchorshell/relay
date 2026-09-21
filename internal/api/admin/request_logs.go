package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/anchorshell/relay/internal/store"
	"github.com/labstack/echo/v5"
)

type RecentFlowActivityResponse struct {
	Requests []store.RecentFlowRequest `json:"requests"`
}

func (a *API) recentFlowActivity(c *echo.Context) error {
	queryScopes, err := a.queryScopes(c, "recent_flow_activity")
	if err != nil {
		return err
	}
	opts := store.RecentFlowRequestOptions{
		Limit:        store.DefaultRecentFlowActivityLimit,
		LaneUUID:     strings.TrimSpace(c.QueryParam("lane_id")),
		EndpointUUID: strings.TrimSpace(c.QueryParam("endpoint_id")),
		QueryScopes:  queryScopes,
	}
	if rawLimit := strings.TrimSpace(c.QueryParam("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be an integer")
		}
		opts.Limit = parsed
	}
	items, err := a.store.ListRecentFlowRequestsWithOptions(c.Request().Context(), opts)
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecentFlowLaneID) {
			return echo.NewHTTPError(http.StatusBadRequest, "lane_id must be a valid id")
		}
		if errors.Is(err, store.ErrInvalidRecentFlowEndpointID) {
			return echo.NewHTTPError(http.StatusBadRequest, "endpoint_id must be a valid id")
		}
		return err
	}
	return c.JSON(http.StatusOK, RecentFlowActivityResponse{Requests: items})
}

func (a *API) requestLogs(c *echo.Context) error {
	queryScopes, err := a.queryScopes(c, "request_logs")
	if err != nil {
		return err
	}
	opts, err := requestLogListOptions(c)
	if err != nil {
		return err
	}
	opts.QueryScopes = queryScopes
	page, err := a.store.ListRequestLogPage(c.Request().Context(), opts)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, page)
}

func requestLogListOptions(c *echo.Context) (store.RequestLogListOptions, error) {
	opts := store.RequestLogListOptions{
		Limit:           store.DefaultRequestLogListLimit,
		Sort:            strings.TrimSpace(c.QueryParam("sort")),
		Direction:       strings.ToLower(strings.TrimSpace(c.QueryParam("direction"))),
		PrimaryAction:   strings.ToLower(strings.TrimSpace(c.QueryParam("primary_action"))),
		Domain:          strings.ToLower(strings.TrimSpace(c.QueryParam("domain"))),
		GuardrailStatus: strings.ToLower(strings.TrimSpace(c.QueryParam("guardrail_status"))),
		GuardrailPreset: strings.ToLower(strings.TrimSpace(c.QueryParam("guardrail_preset"))),
	}
	if raw := strings.TrimSpace(c.QueryParam("guardrail_applied")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return opts, echo.NewHTTPError(http.StatusBadRequest, "guardrail_applied must be true or false")
		}
		opts.GuardrailApplied = &value
	}
	if rawLimit := strings.TrimSpace(c.QueryParam("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil {
			return opts, echo.NewHTTPError(http.StatusBadRequest, "limit must be an integer")
		}
		opts.Limit = limit
	}
	if rawOffset := strings.TrimSpace(c.QueryParam("offset")); rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil {
			return opts, echo.NewHTTPError(http.StatusBadRequest, "offset must be an integer")
		}
		opts.Offset = offset
	}
	if _, _, _, err := store.NormalizeRequestLogListOptions(opts); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidRequestLogSort):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid request log sort")
		case errors.Is(err, store.ErrInvalidRequestLogDirection):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid request log sort direction")
		case errors.Is(err, store.ErrInvalidRequestLogOffset):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid request log offset")
		case errors.Is(err, store.ErrInvalidRequestLogAction):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid request log action")
		case errors.Is(err, store.ErrInvalidRequestLogDomain):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid request log domain")
		case errors.Is(err, store.ErrInvalidGuardrailStatus):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid guardrail status")
		case errors.Is(err, store.ErrInvalidGuardrailPreset):
			return opts, echo.NewHTTPError(http.StatusBadRequest, "invalid guardrail preset")
		default:
			return opts, err
		}
	}
	return opts, nil
}

func (a *API) requestLogDetail(c *echo.Context) error {
	queryScopes, err := a.queryScopes(c, "request_logs")
	if err != nil {
		return err
	}
	log, err := a.store.GetRequestLogWithScopes(c.Request().Context(), c.Param("requestID"), queryScopes)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, log)
}
