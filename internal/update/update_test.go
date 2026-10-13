package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0", "v1.0.0-rc1", true},
		{"v1.0.0-rc1", "v1.0.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"v0.1.0", "dev", true},
	} {
		if Newer(c.a, c.b) != c.want {
			t.Errorf("Newer(%s, %s) != %v", c.a, c.b, c.want)
		}
	}
}

func TestVerify(t *testing.T) {
	if pub, _ := base64.StdEncoding.DecodeString(publicKey); len(pub) != ed25519.PublicKeySize {
		t.Fatal("publicKey is not an Ed25519 key")
	}
	if Verify([]byte("version v1.0.0\n"), []byte(base64.StdEncoding.EncodeToString(make([]byte, 64)))) == nil {
		t.Error("a zero signature passed")
	}
}

func TestPoints(t *testing.T) {
	got := points("# scurvy 0.2\n\n## New\n- 🍋 Enter updates. Press <kbd>enter</kbd>.\n- `scurvy list` prints\n\n**Update:** x")
	if len(got) != 2 || got[0] != "🍋 Enter updates. Press enter." || got[1] != "scurvy list prints" {
		t.Errorf("%q", got)
	}
}
