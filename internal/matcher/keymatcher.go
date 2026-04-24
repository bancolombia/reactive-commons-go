// Package matcher implements RabbitMQ topic-style wildcard matching for
// reactive-commons handler names. The semantics for ranking competing
// patterns mirror reactive-commons-java's KeyMatcher so handler-name
// resolution stays identical across both libraries.
//
// Pattern syntax:
//
//	"*"  matches exactly one dot-separated segment
//	"#"  matches zero or more segments
//
// Segment matching follows RabbitMQ topic-exchange rules: "order.#" matches
// "order", "order.created", and "order.v2.created"; "order.*" matches only
// single-segment extensions like "order.created".
package matcher

import "strings"

const (
	singleWordWildcard   = "*"
	multipleWordWildcard = "#"
	separator            = "."
)

// HasWildcard reports whether s contains any RabbitMQ topic wildcard.
func HasWildcard(s string) bool {
	return strings.ContainsAny(s, "*#")
}

// Matches reports whether routingKey matches pattern under RabbitMQ topic
// rules. A pattern with no wildcard never matches via this function;
// callers should check exact equality first.
func Matches(routingKey, pattern string) bool {
	if !HasWildcard(pattern) {
		return false
	}
	return matchSegments(strings.Split(pattern, separator), strings.Split(routingKey, separator))
}

// matchSegments recursively matches pattern segments against key segments.
// The recursion stays bounded by the number of '#' wildcards in the
// pattern, which is small in practice.
func matchSegments(pattern, key []string) bool {
	if len(pattern) == 0 {
		return len(key) == 0
	}
	head := pattern[0]
	if head == multipleWordWildcard {
		// '#' can match zero or more key segments. Try every split.
		rest := pattern[1:]
		if len(rest) == 0 {
			return true
		}
		for i := 0; i <= len(key); i++ {
			if matchSegments(rest, key[i:]) {
				return true
			}
		}
		return false
	}
	if len(key) == 0 {
		return false
	}
	if head == singleWordWildcard || head == key[0] {
		return matchSegments(pattern[1:], key[1:])
	}
	return false
}

// MostSpecific returns the most specific pattern from candidates using the
// same comparator as reactive-commons-java's KeyMatcher: fewer wildcards
// beats more, '*' beats '#' at the same position, and longer patterns win
// ties. Returns "" when candidates is empty.
func MostSpecific(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if compare(c, best) < 0 {
			best = c
		}
	}
	return best
}

// compare returns a negative number if a is more specific than b, positive
// if less specific, and 0 if equally specific. This mirrors the recursive
// comparator in KeyMatcher.compare from the Java reference.
func compare(a, b string) int {
	aParts := strings.Split(a, separator)
	bParts := strings.Split(b, separator)
	initial := len(bParts) - len(aParts)
	return compareSegments(initial, aParts, bParts, 0)
}

func compareSegments(current int, first, second []string, idx int) int {
	if idx >= len(first) || idx >= len(second) {
		return current
	}
	if first[idx] == second[idx] {
		return compareSegments(current, first, second, idx+1)
	}
	if first[idx] != singleWordWildcard && first[idx] != multipleWordWildcard {
		return -1
	}
	if second[idx] != singleWordWildcard && second[idx] != multipleWordWildcard {
		return 1
	}
	if first[idx] == multipleWordWildcard && second[idx] == singleWordWildcard {
		return compareSegments(1, first, second, idx+1)
	}
	if first[idx] == singleWordWildcard && second[idx] == multipleWordWildcard {
		return compareSegments(-1, first, second, idx+1)
	}
	return len(second) - len(first)
}
