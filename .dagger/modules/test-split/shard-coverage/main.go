// Command shard-coverage checks that test-split's checks run every top-level
// suite of a package exactly once.
//
// Usage:
//
//	shard-coverage SUITES [CHECK RUN SKIP]...
//
// SUITES is the output of `go test -list .` for the package. Each CHECK RUN
// SKIP triple is a check with the -run and -skip patterns it passes to go
// test; an empty pattern is unset.
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type check struct {
	name      string
	run, skip filterMatch
}

func main() {
	if len(os.Args) < 2 || (len(os.Args)-2)%3 != 0 {
		fmt.Fprintln(os.Stderr, "usage: shard-coverage SUITES [CHECK RUN SKIP]...")
		os.Exit(2)
	}
	suites, err := readSuites(os.Args[1])
	if err != nil {
		fatal(err)
	}
	var checks []check
	for a := os.Args[2:]; len(a) > 0; a = a[3:] {
		c := check{name: a[0]}
		if c.run, err = compile(a[1]); err != nil {
			fatal(fmt.Errorf("%s: -run: %w", c.name, err))
		}
		if c.skip, err = compile(a[2]); err != nil {
			fatal(fmt.Errorf("%s: -skip: %w", c.name, err))
		}
		checks = append(checks, c)
	}

	var problems []string
	for _, suite := range suites {
		var in []string
		for _, c := range checks {
			if c.selects(suite) {
				in = append(in, c.name)
			}
		}
		switch {
		case len(in) == 0:
			problems = append(problems, fmt.Sprintf("%s runs in no check", suite))
		case len(in) > 1:
			problems = append(problems, fmt.Sprintf("%s runs in more than one check: %v", suite, in))
		}
		fmt.Printf("%-40s %s\n", suite, strings.Join(in, " "))
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d of %d suites are not assigned to exactly one check:\n", len(problems), len(suites))
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		os.Exit(1)
	}
	fmt.Printf("\nall %d suites run in exactly one check\n", len(suites))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func readSuites(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var suites []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); strings.HasPrefix(name, "Test") {
			suites = append(suites, name)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(suites) == 0 {
		return nil, fmt.Errorf("%s: no suites listed", path)
	}
	return suites, nil
}

// selects reports whether go test runs the top-level test name with the
// check's -run and -skip, following testing.matcher.fullName.
func (c check) selects(name string) bool {
	if c.run != nil {
		if ok, _ := c.run.matches(name); !ok {
			return false
		}
	}
	if c.skip != nil {
		if ok, partial := c.skip.matches(name); ok && !partial {
			return false
		}
	}
	return true
}

// The rest mirrors the testing package's pattern handling (testing/match.go):
// a pattern splits into '|' alternatives of '/'-separated levels, and each
// level matches its test name unanchored. go test also rewrites whitespace
// and non-printable characters in a pattern before matching; this helper
// rejects such patterns instead.

type filterMatch interface {
	// matches reports whether the top-level name matches, and whether the
	// match is partial: the pattern has further levels for subtests.
	matches(name string) (ok, partial bool)
}

type simpleMatch []*regexp.Regexp

func (m simpleMatch) matches(name string) (ok, partial bool) {
	return m[0].MatchString(name), len(m) > 1
}

type alternationMatch []filterMatch

func (m alternationMatch) matches(name string) (ok, partial bool) {
	for _, m := range m {
		if ok, partial = m.matches(name); ok {
			return ok, partial
		}
	}
	return false, false
}

func compile(pattern string) (filterMatch, error) {
	if pattern == "" {
		return nil, nil
	}
	if strings.ContainsFunc(pattern, func(r rune) bool {
		return unicode.IsSpace(r) || !strconv.IsPrint(r)
	}) {
		return nil, fmt.Errorf("%q: whitespace and non-printable characters are not supported", pattern)
	}
	var alts alternationMatch
	for _, alt := range splitRegexp(pattern) {
		var m simpleMatch
		for _, level := range alt {
			re, err := regexp.Compile(level)
			if err != nil {
				return nil, err
			}
			m = append(m, re)
		}
		alts = append(alts, m)
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return alts, nil
}

// splitRegexp splits a pattern into '|' alternatives of '/'-separated levels,
// ignoring separators inside brackets, parentheses or after a backslash.
func splitRegexp(s string) [][]string {
	var alts [][]string
	var levels []string
	cs, cp := 0, 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			cs++
		case ']':
			if cs--; cs < 0 {
				cs = 0
			}
		case '(':
			if cs == 0 {
				cp++
			}
		case ')':
			if cs == 0 {
				cp--
			}
		case '\\':
			i++
		case '/', '|':
			if cs == 0 && cp == 0 {
				levels = append(levels, s[:i])
				if s[i] == '|' {
					alts = append(alts, levels)
					levels = nil
				}
				s = s[i+1:]
				i = 0
				continue
			}
		}
		i++
	}
	return append(alts, append(levels, s))
}
