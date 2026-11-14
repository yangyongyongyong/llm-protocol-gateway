package desktopupdate

import (
	"cmp"
	"strconv"
	"strings"
)

type parsedVersion struct {
	nums [3]int
	pre  string
}

// Newer reports whether a is a later semantic version than b. It accepts the
// leading "v" and ignores build metadata. Invalid or development versions are
// never considered newer: a source build must not silently replace itself.
func Newer(a, b string) bool {
	x, ok := parseVersion(a)
	if !ok {
		return false
	}
	y, ok := parseVersion(b)
	if !ok {
		return false
	}
	for i := range x.nums {
		if x.nums[i] != y.nums[i] {
			return x.nums[i] > y.nums[i]
		}
	}
	return comparePrerelease(x.pre, y.pre) > 0
}

func parseVersion(v string) (parsedVersion, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return parsedVersion{}, false
	}
	var out parsedVersion
	for i, part := range parts {
		if part == "" {
			return parsedVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return parsedVersion{}, false
		}
		out.nums[i] = n
	}
	out.pre = pre
	return out, true
}

// comparePrerelease follows semver ordering: a release comes after any
// prerelease of the same core version, numeric identifiers compare as numbers,
// and a numeric identifier sorts before a word.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.ParseUint(as[i], 10, 64)
		bn, berr := strconv.ParseUint(bs[i], 10, 64)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return cmp.Compare(an, bn)
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(as), len(bs))
}
