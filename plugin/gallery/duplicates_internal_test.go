package gallery

import (
	"math/rand/v2"
	"testing"

	"github.com/ajdnik/imghash/hashtype"
	"github.com/ajdnik/imghash/similarity"
)

func TestHammingWordsMatchesBytes(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{8, 13, 32} {
		for range 100 {
			a, b := make(hashtype.Binary, n), make(hashtype.Binary, n)
			for i := range n {
				a[i], b[i] = byte(r.Uint32()), byte(r.Uint32())
			}
			if got, want := hammingWords(packWords(a), packWords(b)), int(similarity.Hamming(a, b)); got != want {
				t.Fatalf("%d bytes: got %d, want %d", n, got, want)
			}
		}
	}
}
