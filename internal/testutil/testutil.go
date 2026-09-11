// Package testutil provides shared test helpers.
package testutil

// Ptr returns a pointer to the given value.
// Replaces the duplicated intPtr/strPtr/boolPtr helpers across test packages.
func Ptr[T any](v T) *T { return &v }
