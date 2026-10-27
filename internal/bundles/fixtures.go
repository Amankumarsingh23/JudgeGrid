package bundles

// Demo contains trusted problem-setter fixtures, never served by the public API.
func Demo(slug string) (Bundle, bool) {
	var pairs [][2]string
	switch slug {
	case "two-sum":
		pairs = [][2]string{{"2 3\n", "5\n"}, {"10 20\n", "30\n"}, {"-4 7\n", "3\n"}, {"0 0\n", "0\n"}, {"-1000000000 -1000000000\n", "-2000000000\n"}, {"1000000000 1000000000\n", "2000000000\n"}}
	case "maximum":
		pairs = [][2]string{{"1 2 3\n", "3\n"}, {"-9 -2 -5\n", "-2\n"}, {"0 0 0\n", "0\n"}, {"7 7 -1\n", "7\n"}, {"-1000000000 1000000000 0\n", "1000000000\n"}, {"3 2 1\n", "3\n"}}
	default:
		return Bundle{}, false
	}
	b := Bundle{Schema: 1, Comparator: "tokens"}
	for i, p := range pairs {
		id := string(rune('a' + i))
		b.Tests = append(b.Tests, Case{ID: "case-" + id, Order: i + 1, Sample: i == 0, Input: []byte(p[0]), Expected: []byte(p[1])})
	}
	return b, true
}
