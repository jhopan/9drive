package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func newJSONBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func jsonField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatal(err)
	}
	value, _ := data[field].(string)
	return value
}
