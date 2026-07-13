package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func (c *compiledConfig) match(status int, path string, body []byte) *compiledRule {
	var decoded any
	jsonDecoded := false

	for i := range c.rules {
		rule := &c.rules[i]
		if _, ok := rule.statusSet[status]; !ok {
			continue
		}
		if !rule.matchesPath(path) {
			continue
		}

		jsonMatch := len(rule.JSONPaths) == 0
		if len(rule.JSONPaths) > 0 {
			if !jsonDecoded {
				decoded = decodeJSONBody(body)
				jsonDecoded = true
			}
			jsonMatch = rule.matchesJSON(decoded)
		}

		messageMatch := len(rule.msgContains) == 0
		if len(rule.msgContains) > 0 {
			if !jsonDecoded {
				decoded = decodeJSONBody(body)
				jsonDecoded = true
			}
			messageMatch = rule.matchesMessage(decoded)
		}

		bodyMatch := len(rule.bodyContains) == 0
		if len(rule.bodyContains) > 0 {
			bodyMatch = rule.matchesBody(body)
		}

		// When both groups are configured, both must match. Values within a
		// group use OR semantics.
		if jsonMatch && messageMatch && bodyMatch {
			return rule
		}
	}

	return nil
}

func decodeJSONBody(body []byte) any {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil
	}
	return decoded
}

func (r *compiledRule) matchesPath(path string) bool {
	if len(r.URLPathPrefixes) == 0 && len(r.URLPathContains) == 0 {
		return true
	}
	for _, prefix := range r.URLPathPrefixes {
		if prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, value := range r.URLPathContains {
		if value != "" && strings.Contains(path, value) {
			return true
		}
	}
	return false
}

func (r *compiledRule) matchesJSON(root any) bool {
	if root == nil {
		return false
	}
	for _, path := range r.JSONPaths {
		value, ok := lookupJSONPath(root, path)
		if !ok {
			continue
		}
		text := scalarString(value)
		if r.CaseInsensitive {
			text = strings.ToLower(text)
		}
		if _, ok := r.valueSet[text]; ok {
			return true
		}
	}
	return false
}

func (r *compiledRule) matchesMessage(root any) bool {
	if root == nil {
		return false
	}
	for _, path := range r.MessagePaths {
		value, ok := lookupJSONPath(root, path)
		if !ok {
			continue
		}
		text := scalarString(value)
		if r.CaseInsensitive {
			text = strings.ToLower(text)
		}
		for _, needle := range r.msgContains {
			if strings.Contains(text, needle) {
				return true
			}
		}
	}
	return false
}

func (r *compiledRule) matchesBody(body []byte) bool {
	if r.CaseInsensitive {
		body = bytes.ToLower(body)
	}
	for _, needle := range r.bodyContains {
		if bytes.Contains(body, needle) {
			return true
		}
	}
	return false
}

func lookupJSONPath(root any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return root, true
	}

	var parts []string
	if strings.HasPrefix(path, "/") {
		for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			part = strings.ReplaceAll(part, "~1", "/")
			part = strings.ReplaceAll(part, "~0", "~")
			parts = append(parts, part)
		}
	} else {
		path = strings.TrimPrefix(path, "$.")
		parts = strings.Split(path, ".")
	}

	current := root
	for _, part := range parts {
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[part]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	default:
		return fmt.Sprint(v)
	}
}
