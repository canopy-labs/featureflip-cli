package importer

import "strings"

// maxKeyLength is the longest flag or segment key the API accepts.
const maxKeyLength = 255

// NormalizeKey turns a source key into one the API accepts
// (^[a-z0-9]+([_-][a-z0-9]+)*$): it lowercases, turns every other character
// into "-", keeps one separator between words and trims them from the ends.
// It returns "" when nothing usable is left.
func NormalizeKey(s string) string {
	var b strings.Builder
	pendingSep := byte(0)
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingSep != 0 && b.Len() > 0 {
				b.WriteByte(pendingSep)
			}
			pendingSep = 0
			b.WriteRune(r)
		case r == '_' || r == '-':
			if pendingSep == 0 {
				pendingSep = byte(r)
			}
		default:
			if pendingSep == 0 {
				pendingSep = '-'
			}
		}
	}
	key := b.String()
	if len(key) > maxKeyLength {
		key = strings.TrimRight(key[:maxKeyLength], "_-")
	}
	return key
}

// Keyer hands out normalized keys and reports collisions: two source keys
// that normalize to the same key can't both be imported under it.
type Keyer struct {
	owners map[string][]string
}

func NewKeyer() *Keyer { return &Keyer{owners: map[string][]string{}} }

// Key returns source's normalized key and registers source as one of its owners.
func (k *Keyer) Key(source string) string {
	key := NormalizeKey(source)
	k.owners[key] = append(k.owners[key], source)
	return key
}

// Owners lists every source key that normalized to key.
func (k *Keyer) Owners(key string) []string { return k.owners[key] }
