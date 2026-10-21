// A scalar module workload with explicit, identical file I/O in every arm.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

type Worker struct{}

func (*Worker) Work(value string) (string, error) {
	b, err := os.ReadFile("input")
	if err != nil {
		return "", err
	}
	b = append(b, []byte(value)...)
	h := sha256.Sum256(b)
	out := hex.EncodeToString(h[:])
	if err := os.WriteFile("output", []byte(out), 0600); err != nil {
		return "", err
	}
	if err := os.Remove("output"); err != nil {
		return "", err
	}
	return out, nil
}
