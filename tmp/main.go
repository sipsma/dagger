package main

import (
	"crypto/rand"
	"time"
)

type Test struct{}

// My cool doc on TestTtl
// +ttl=30
func (m *Test) TestTtl() int {
	return int(time.Now().Unix())
}

// My dope doc on TestNeverCache
// +nocache
func (m *Test) TestNeverCache() string {
	return rand.Text()
}

// My rad doc on TestAlwaysCache
func (m *Test) TestAlwaysCache() string {
	return rand.Text()
}
