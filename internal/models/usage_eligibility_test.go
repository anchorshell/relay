package models

import (
	"net/http"
	"testing"
)

func TestCountsAsUsageCountsCompletedProviderResponsesRegardlessOfStatus(t *testing.T) {
	for _, statusCode := range []int{http.StatusOK, http.StatusBadRequest, http.StatusServiceUnavailable} {
		if !CountsAsUsage("completed", statusCode, 0, 0, 0, 0) {
			t.Fatalf("expected completed provider response %d to consume a request", statusCode)
		}
	}
}

func TestCountsAsUsageExcludesPreDispatchFailureWithoutActualUsage(t *testing.T) {
	if CountsAsUsage("failed", http.StatusServiceUnavailable, 0, 0, 0, 0) {
		t.Fatal("expected a failed request with no provider usage to remain outside usage windows")
	}
}

func TestCountsAsUsagePreservesActualUsageOnOtherTerminalStates(t *testing.T) {
	if !CountsAsUsage("failed", http.StatusBadGateway, 3, 1, 2, 0) {
		t.Fatal("expected actual upstream usage to remain accountable after terminal failure")
	}
}
