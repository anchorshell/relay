package guardrails

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var ruleIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ParseRuleSet(raw string) (RuleSet, error) {
	var rules RuleSet
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&rules); err != nil {
		return rules, fmt.Errorf("invalid response rules: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return rules, errors.New("response rules must contain exactly one JSON value")
	}
	if err := ValidateRuleSet(rules); err != nil {
		return rules, err
	}
	return rules, nil
}

func ValidateRuleSet(set RuleSet) error {
	if set.MatchMode == "" {
		set.MatchMode = "any"
	}
	if set.MatchMode != "any" && set.MatchMode != "all" {
		return errors.New("match_mode must be any or all")
	}
	if len(set.Rules) == 0 {
		return errors.New("at least one response rule is required")
	}
	seen := map[string]struct{}{}
	for _, rule := range set.Rules {
		if !ruleIDPattern.MatchString(rule.ID) {
			return fmt.Errorf("invalid rule id %q", rule.ID)
		}
		if _, ok := seen[rule.ID]; ok {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		if rule.Source != "json_body" && rule.Source != "http_status" && rule.Source != "response_header" {
			return fmt.Errorf("invalid source for rule %s", rule.ID)
		}
		if rule.Source == "json_body" {
			if _, err := parsePath(rule.Path); err != nil {
				return fmt.Errorf("rule %s: %w", rule.ID, err)
			}
		}
		if rule.Source == "response_header" && strings.TrimSpace(rule.Header) == "" {
			return fmt.Errorf("rule %s requires a header", rule.ID)
		}
		if err := validateOperator(rule.Operator, rule.Value); err != nil {
			return fmt.Errorf("rule %s: %w", rule.ID, err)
		}
	}
	if set.MissingPath == "" {
		set.MissingPath = "error"
	}
	if set.MissingPath != "error" && set.MissingPath != "no_match" {
		return errors.New("missing_path must be error or no_match")
	}
	if err := validateRuleAction(set.OnMatch, true); err != nil {
		return fmt.Errorf("on_match: %w", err)
	}
	if set.OnNoMatch.Action == "" {
		set.OnNoMatch.Action = DecisionAllow
	}
	if err := validateRuleAction(set.OnNoMatch, false); err != nil {
		return fmt.Errorf("on_no_match: %w", err)
	}
	return nil
}

func validateOperator(operator string, value any) error {
	switch operator {
	case "exists", "not_exists", "truthy", "falsy":
		return nil
	case "equals", "not_equals", "contains", "not_contains", "in", "not_in":
		return nil
	case "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		if _, ok := number(value); !ok {
			return errors.New("numeric operator requires a numeric value")
		}
		return nil
	case "matches_regex":
		pattern, ok := value.(string)
		if !ok {
			return errors.New("regex operator requires a string value")
		}
		_, err := regexp.Compile(pattern)
		return err
	default:
		return fmt.Errorf("unsupported operator %q", operator)
	}
}

func validateRuleAction(action RuleAction, matched bool) error {
	if action.Action == "" && !matched {
		return nil
	}
	switch action.Action {
	case DecisionAllow, DecisionLogOnly:
		return nil
	case DecisionBlock:
		if action.HTTPStatus != 0 && (action.HTTPStatus < 400 || action.HTTPStatus > 599) {
			return errors.New("HTTP status must be between 400 and 599")
		}
		return nil
	case DecisionReplaceResponse:
		if strings.TrimSpace(action.ReplacementText) == "" && strings.TrimSpace(action.ReplacementPath) == "" {
			return errors.New("replacement text or path is required")
		}
		if action.ReplacementPath != "" {
			_, err := parsePath(action.ReplacementPath)
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported action %q", action.Action)
	}
}

func EvaluateRules(set RuleSet, status int, headers http.Header, body any) (RuleAction, []RuleResult, error) {
	results := make([]RuleResult, 0, len(set.Rules))
	matchedCount := 0
	for _, rule := range set.Rules {
		value, exists, err := ruleValue(rule, status, headers, body)
		if err != nil {
			return RuleAction{}, results, err
		}
		if !exists && rule.Operator != "exists" && rule.Operator != "not_exists" {
			if set.MissingPath != "no_match" {
				return RuleAction{}, results, fmt.Errorf("required response path missing for rule %s", rule.ID)
			}
			results = append(results, RuleResult{ID: rule.ID, Label: rule.Label, Source: rule.Source, Path: rule.Path, Operator: rule.Operator, Expected: rule.Value})
			continue
		}
		matched, err := compare(value, exists, rule.Operator, rule.Value)
		if err != nil {
			return RuleAction{}, results, fmt.Errorf("rule %s: %w", rule.ID, err)
		}
		if matched {
			matchedCount++
		}
		results = append(results, RuleResult{ID: rule.ID, Label: rule.Label, Source: rule.Source, Path: rule.Path, Operator: rule.Operator, Actual: value, Expected: rule.Value, Matched: matched})
	}
	matched := matchedCount > 0
	if set.MatchMode == "all" {
		matched = matchedCount == len(set.Rules)
	}
	if matched {
		return set.OnMatch, results, nil
	}
	action := set.OnNoMatch
	if action.Action == "" {
		action.Action = DecisionAllow
	}
	return action, results, nil
}

func ruleValue(rule Rule, status int, headers http.Header, body any) (any, bool, error) {
	switch rule.Source {
	case "http_status":
		return status, true, nil
	case "response_header":
		values, ok := headers[http.CanonicalHeaderKey(rule.Header)]
		if !ok {
			return nil, false, nil
		}
		if len(values) == 1 {
			return values[0], true, nil
		}
		return values, true, nil
	case "json_body":
		return resolvePath(body, rule.Path)
	default:
		return nil, false, errors.New("unsupported rule source")
	}
}

func compare(actual any, exists bool, operator string, expected any) (bool, error) {
	switch operator {
	case "exists":
		return exists, nil
	case "not_exists":
		return !exists, nil
	case "truthy":
		return truthy(actual), nil
	case "falsy":
		return !truthy(actual), nil
	case "equals":
		return valuesEqual(actual, expected), nil
	case "not_equals":
		return !valuesEqual(actual, expected), nil
	case "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		a, okA := number(actual)
		b, okB := number(expected)
		if !okA || !okB {
			return false, errors.New("numeric comparison received a non-numeric value")
		}
		switch operator {
		case "greater_than":
			return a > b, nil
		case "greater_than_or_equal":
			return a >= b, nil
		case "less_than":
			return a < b, nil
		default:
			return a <= b, nil
		}
	case "contains", "not_contains":
		contains := false
		switch typed := actual.(type) {
		case string:
			contains = strings.Contains(typed, fmt.Sprint(expected))
		case []any:
			for _, item := range typed {
				if valuesEqual(item, expected) {
					contains = true
					break
				}
			}
		case []string:
			for _, item := range typed {
				if valuesEqual(item, expected) {
					contains = true
					break
				}
			}
		default:
			return false, errors.New("contains requires a string or array")
		}
		if operator == "not_contains" {
			contains = !contains
		}
		return contains, nil
	case "matches_regex":
		actualString, ok := actual.(string)
		if !ok {
			return false, errors.New("regex requires a string response value")
		}
		pattern, _ := expected.(string)
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return false, err
		}
		return compiled.MatchString(actualString), nil
	case "in", "not_in":
		values, ok := expected.([]any)
		if !ok {
			return false, errors.New("in requires an array value")
		}
		found := false
		for _, item := range values {
			if valuesEqual(actual, item) {
				found = true
				break
			}
		}
		if operator == "not_in" {
			found = !found
		}
		return found, nil
	default:
		return false, errors.New("unsupported operator")
	}
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case json.Number:
		n, _ := typed.Float64()
		return n != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func valuesEqual(a, b any) bool {
	if av, ok := number(a); ok {
		if bv, ok := number(b); ok {
			return av == bv
		}
	}
	return reflect.DeepEqual(a, b) || fmt.Sprint(a) == fmt.Sprint(b)
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case json.Number:
		v, err := typed.Float64()
		return v, err == nil
	case string:
		v, err := strconv.ParseFloat(typed, 64)
		return v, err == nil
	default:
		return 0, false
	}
}

type pathToken struct {
	key         *string
	index       *int
	filterKey   string
	filterValue any
}

func parsePath(path string) ([]pathToken, error) {
	if path == "$" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "$") {
		return nil, errors.New("JSON path must start with $")
	}
	var tokens []pathToken
	for i := 1; i < len(path); {
		switch path[i] {
		case '.':
			i++
			start := i
			for i < len(path) && (path[i] == '_' || path[i] == '-' || path[i] >= 'a' && path[i] <= 'z' || path[i] >= 'A' && path[i] <= 'Z' || path[i] >= '0' && path[i] <= '9') {
				i++
			}
			if start == i {
				return nil, errors.New("invalid object segment in JSON path")
			}
			key := path[start:i]
			tokens = append(tokens, pathToken{key: &key})
		case '[':
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				return nil, errors.New("unterminated JSON path bracket")
			}
			end += i
			inside := strings.TrimSpace(path[i+1 : end])
			if strings.HasPrefix(inside, `"`) {
				var key string
				if err := json.Unmarshal([]byte(inside), &key); err != nil || key == "" {
					return nil, errors.New("quoted object key must be a non-empty JSON string")
				}
				tokens = append(tokens, pathToken{key: &key})
			} else if strings.HasPrefix(inside, "?(@.") {
				match := regexp.MustCompile(`^\?\(@\.([A-Za-z0-9_-]+)\s*==\s*(.+)\)$`).FindStringSubmatch(inside)
				if len(match) != 3 {
					return nil, errors.New("only equality array filters are supported")
				}
				var filterValue any
				if err := json.Unmarshal([]byte(match[2]), &filterValue); err != nil {
					return nil, errors.New("array filter value must be JSON")
				}
				tokens = append(tokens, pathToken{filterKey: match[1], filterValue: filterValue})
			} else {
				index, err := strconv.Atoi(inside)
				if err != nil || index < 0 {
					return nil, errors.New("array index must be a non-negative integer")
				}
				tokens = append(tokens, pathToken{index: &index})
			}
			i = end + 1
		default:
			return nil, errors.New("invalid JSON path syntax")
		}
	}
	return tokens, nil
}

func resolvePath(root any, path string) (any, bool, error) {
	tokens, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}
	current := root
	for _, token := range tokens {
		if token.key != nil {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			current, ok = object[*token.key]
			if !ok {
				return nil, false, nil
			}
			continue
		}
		array, ok := current.([]any)
		if !ok {
			return nil, false, nil
		}
		if token.index != nil {
			if *token.index >= len(array) {
				return nil, false, nil
			}
			current = array[*token.index]
			continue
		}
		found := false
		for _, item := range array {
			object, ok := item.(map[string]any)
			if ok && valuesEqual(object[token.filterKey], token.filterValue) {
				current = item
				found = true
				break
			}
		}
		if !found {
			return nil, false, nil
		}
	}
	return current, true, nil
}
