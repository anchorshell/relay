package models

import "net/http"

func CountsAsUsage(taskState string, statusCode int, actualTotalTokens, actualInputTokens, actualOutputTokens, actualCostMicros int64) bool {
	return CompletedProviderResponse(taskState) || (TerminalUsageState(taskState) && HasActualUsage(actualTotalTokens, actualInputTokens, actualOutputTokens, actualCostMicros))
}

// CompletedProviderResponse identifies work which reached an upstream provider
// and received a terminal HTTP response. Provider request limiters normally
// consume the request before the model returns a 2xx, 4xx, or 5xx status, so
// request windows count all of those responses. Work rejected before dispatch
// remains failed/queued rather than completed and is deliberately excluded.
func CompletedProviderResponse(taskState string) bool {
	return taskState == "completed"
}

func TerminalUsageState(taskState string) bool {
	switch taskState {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func SuccessfulUsage(taskState string, statusCode int) bool {
	return taskState == "completed" && statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices
}

func HasActualUsage(actualTotalTokens, actualInputTokens, actualOutputTokens, actualCostMicros int64) bool {
	return actualTotalTokens > 0 || actualInputTokens > 0 || actualOutputTokens > 0 || actualCostMicros > 0
}

func RequestLogCountsAsUsage(log RequestLog) bool {
	return CountsAsUsage(log.TaskState, log.StatusCode, log.ActualTotalTokens, log.ActualInputTokens, log.ActualOutputTokens, log.ActualCostMicros)
}

func RequestLogSuccessfulUsage(log RequestLog) bool {
	return SuccessfulUsage(log.TaskState, log.StatusCode)
}

func RequestLogActualTokens(log RequestLog) int64 {
	if log.ActualTotalTokens > 0 {
		return log.ActualTotalTokens
	}
	return log.ActualInputTokens + log.ActualOutputTokens
}
