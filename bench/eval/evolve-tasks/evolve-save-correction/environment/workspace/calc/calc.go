// Package calc does integer arithmetic.
package calc

import "errors"

// Add returns a plus b.
func Add(a, b int) int {
	return a + b
}

// Subtract returns a minus b.
func Subtract(a, b int) int {
	return a - b
}

// Root returns the integer square root of n.
func Root(n int) (int, error) {
	if n < 0 {
		return 0, errors.New("negative input")
	}

	root := 0
	for (root+1)*(root+1) <= n {
		root++
	}

	return root, nil
}
