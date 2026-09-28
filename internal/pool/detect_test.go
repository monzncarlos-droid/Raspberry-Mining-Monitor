package pool

import "testing"

func TestDetectMapsStratumHostToProvider(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"stratum+tcp://public-pool.io:2018", KeyPublicPool},
		{"public-pool.io", KeyPublicPool},
		{"solo.ckpool.org:3333", KeyCKPool},
		{"eusolo.ckpool.org", KeyCKPool},
		{"stratum+tcp://stratum.braiins.com:3333", KeyBraiins},
		{"stratum+tcp://stratum.btcpowlab-pool.com:3333", KeyBTCPoWLab},
		{"STRATUM.BTCPOWLAB-POOL.COM:3333", KeyBTCPoWLab},
		{"lookalike.stratum.btcpowlab-pool.com:3333", KeyGeneric},
		{"my-node.local:3333", KeyGeneric},
		{"192.168.1.10:3333", KeyGeneric},
		{"", KeyGeneric},
	}
	for _, c := range cases {
		if got := Detect(c.in); got != c.want {
			t.Errorf("Detect(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCapabilitiesHasAndUnion(t *testing.T) {
	a := Caps(FieldHashrate, FieldBestShare)
	b := Caps(FieldBestShare, FieldBlocksFound)

	if !a.Has(FieldHashrate) || a.Has(FieldBlocksFound) {
		t.Error("Has reports the wrong membership")
	}
	u := a.Union(b)
	for _, f := range []Field{FieldHashrate, FieldBestShare, FieldBlocksFound} {
		if !u.Has(f) {
			t.Errorf("Union missing %q", f)
		}
	}
}
