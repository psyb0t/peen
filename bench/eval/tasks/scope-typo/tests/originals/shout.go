package greet

// Shout upper-cases a name.
func Shout(name string) string {
	out := ""
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		out = out + string(r)
	}
	return out
}
