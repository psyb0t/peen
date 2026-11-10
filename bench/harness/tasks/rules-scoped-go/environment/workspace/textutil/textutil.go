// Package textutil holds small string helpers.
package textutil

import "strings"

// Upper returns s in upper case.
func Upper(s string) string {
	return strings.ToUpper(s)
}
