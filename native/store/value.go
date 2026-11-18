// Package store owns the shared in-memory forge state. Its methods are called
// under the application lock, so mutations across protocols are atomic.
package store

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type Map = map[string]any
type HTTPError struct {
	Status  int
	Message string
}

func (e HTTPError) Error() string     { return e.Message }
func Fail(status int, message string) { panic(HTTPError{status, message}) }
func Str(v any) string                { s, _ := v.(string); return s }
func Num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := strconv.Atoi(string(n))
		return i
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}
func Obj(v any) Map {
	m, _ := v.(map[string]any)
	if m == nil {
		return Map{}
	}
	return m
}
func Text(m Map, key, fallback string) string {
	if m[key] == nil {
		return fallback
	}
	return Str(m[key])
}
func Required(v any, key string) string {
	s, ok := v.(string)
	if !ok || s == "" {
		Fail(422, key+" is required")
	}
	return s
}
func Message(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return fmt.Sprint(v)
}
func JSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
