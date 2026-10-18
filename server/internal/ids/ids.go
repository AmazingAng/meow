// Package ids generates resource identifiers and bearer secrets.
package ids

import (
	"crypto/rand"
	"encoding/base32"
)

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.24
	return enc.EncodeToString(b)
}

// New returns a public identifier such as "qst_3k2j4h5g6f7d8s9a".
func New(prefix string) string { return prefix + "_" + random(10) }

// Secret returns a 192-bit bearer secret such as "meow_…".
func Secret(prefix string) string { return prefix + "_" + random(24) }
