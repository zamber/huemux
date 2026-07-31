package appconfig

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// TokenSeparator joins the words of a generated token. A dot rather than a
// dash or space because it survives being pasted into a URL query parameter
// (?token=...) without escaping, which is the transport a browser WebSocket
// is stuck with — WebSocket cannot set an Authorization header.
const TokenSeparator = "."

// DefaultTokenWords is how many words GenerateToken produces when not told
// otherwise. Three is short enough to read down a phone line and retype on a
// TV remote, which is the whole design goal; see TokenEntropyBits for the
// security cost of that choice and when to raise it.
const DefaultTokenWords = 3

// MinTokenWords guards against a config that requests a trivially guessable
// token. One or two words is not a credential.
const MinTokenWords = 3

// GenerateToken returns a token of n dot-separated words drawn uniformly,
// with replacement, from tokenWords using crypto/rand.
//
// Words repeat with probability ~n²/2N (about 1% at n=3), which is left
// deliberately possible: rejecting duplicates would remove entropy rather
// than add it, and "otter.otter.beacon" is a perfectly good token.
func GenerateToken(n int) (string, error) {
	if n < MinTokenWords {
		return "", fmt.Errorf("token word count %d is below the minimum of %d", n, MinTokenWords)
	}
	words := make([]string, n)
	for i := range words {
		w, err := tokenWord()
		if err != nil {
			return "", err
		}
		words[i] = w
	}
	return strings.Join(words, TokenSeparator), nil
}

// tokenWord returns one uniformly random word. rand.Int is used rather than
// masking a byte because len(tokenWords) is not a power of two, and taking a
// byte modulo a non-power-of-two silently biases the low-numbered words.
func tokenWord() (string, error) {
	max := big.NewInt(int64(len(tokenWords)))
	idx, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("read random source: %w", err)
	}
	return tokenWords[idx.Int64()], nil
}

// TokenEntropyBits reports the entropy of an n-word token from this wordlist,
// as log2(len(tokenWords)^n).
//
// With the current list this is roughly 8.6 bits per word, so the default
// three words is about 26 bits — around 67 million possibilities. That is
// appropriate for a LAN service behind failed-attempt rate limiting, where an
// online attacker gets a few guesses a minute, and it is *not* appropriate for
// anything reachable from the internet, where it would fall in hours. Raise
// the word count for wider exposure: five words is about 43 bits.
//
// This is why the recommended deployment order in plans/01 puts Tailscale
// ahead of port-forwarding — it removes the need for this number to be large.
func TokenEntropyBits(n int) float64 {
	if n <= 0 || len(tokenWords) == 0 {
		return 0
	}
	return float64(n) * math.Log2(float64(len(tokenWords)))
}

// TokenMatches reports whether got equals want, in constant time.
//
// Constant-time because a naive == leaks how many leading bytes were correct
// through its early return, which over many attempts recovers the token one
// character at a time. The length check before it is not a leak worth caring
// about: token length is a config choice, not a secret.
func TokenMatches(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
