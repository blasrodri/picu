package main

import (
	"encoding/json"
	"fmt"
)

// validateDoc checks a JSON document against a minimal subset of JSON
// Schema: top-level required[], and properties with type and minLength.
// Unknown keywords are ignored. Returns human-readable violations.
func validateDoc(schemaRaw []byte, doc map[string]any) ([]string, error) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type      string `json:"type"`
			MinLength *int   `json:"minLength"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, fmt.Errorf("bad schema file: %v", err)
	}
	var bad []string
	for _, name := range schema.Required {
		v, ok := doc[name]
		if !ok || v == nil {
			bad = append(bad, fmt.Sprintf("missing required field %q", name))
			continue
		}
		if spec, known := schema.Properties[name]; known {
			if msg := checkType(name, v, spec.Type, spec.MinLength); msg != "" {
				bad = append(bad, msg)
			}
		}
	}
	for name, spec := range schema.Properties {
		v, ok := doc[name]
		if !ok || v == nil || spec.Type == "" {
			continue
		}
		already := false
		for _, r := range schema.Required {
			if r == name {
				already = true
			}
		}
		if !already {
			if msg := checkType(name, v, spec.Type, spec.MinLength); msg != "" {
				bad = append(bad, msg)
			}
		}
	}
	return bad, nil
}

func checkType(name string, v any, typ string, minLength *int) string {
	switch typ {
	case "string":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("field %q must be string", name)
		}
		if minLength != nil && len(s) < *minLength {
			return fmt.Sprintf("field %q shorter than minLength %d", name, *minLength)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Sprintf("field %q must be number", name)
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int(f)) {
			return fmt.Sprintf("field %q must be integer", name)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("field %q must be boolean", name)
		}
	}
	return ""
}

func schemaDefault() []byte {
	return []byte(`{
  "required": ["url", "title", "markdown"],
  "properties": {
    "url": {"type": "string"},
    "title": {"type": "string"},
    "markdown": {"type": "string", "minLength": 1}
  }
}`)
}
