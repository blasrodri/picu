package main

import "testing"

func TestValidateDoc(t *testing.T) {
	schema := []byte(`{
	  "required": ["url", "markdown"],
	  "properties": {
	    "url": {"type": "string"},
	    "markdown": {"type": "string", "minLength": 10},
	    "chars": {"type": "integer"}
	  }
	}`)
	good := map[string]any{"url": "https://x.test", "markdown": "plenty of content here", "chars": float64(22)}
	if bad, err := validateDoc(schema, good); err != nil || len(bad) != 0 {
		t.Errorf("valid doc rejected: %v %v", bad, err)
	}
	missing := map[string]any{"url": "https://x.test"}
	if bad, _ := validateDoc(schema, missing); len(bad) == 0 {
		t.Error("missing required field accepted")
	}
	short := map[string]any{"url": "https://x.test", "markdown": "tiny"}
	if bad, _ := validateDoc(schema, short); len(bad) == 0 {
		t.Error("short markdown accepted")
	}
	wrongType := map[string]any{"url": "https://x.test", "markdown": "long enough content", "chars": "many"}
	if bad, _ := validateDoc(schema, wrongType); len(bad) == 0 {
		t.Error("wrong type accepted")
	}
	if _, err := validateDoc([]byte("{nope"), good); err == nil {
		t.Error("malformed schema accepted")
	}
}
