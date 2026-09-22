package product

func clipped(value string, limit int) string {
	r := []rune(value)
	if len(r) > limit {
		return string(r[:limit]) + "\n…"
	}
	return value
}
