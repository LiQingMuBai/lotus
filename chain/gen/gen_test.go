package gen

import (
	"testing"

	"github.com/filecoin-project/specs-actors/actors/abi"

	_ "github.com/filecoin-project/lotus/lib/sigs/bls"
	_ "github.com/filecoin-project/lotus/lib/sigs/secp"

	"github.com/filecoin-project/lotus/build"

	"github.com/filecoin-project/specs-actors/actors/abi/big"
	"github.com/filecoin-project/specs-actors/actors/builtin/power"
)

func init() {
	build.SectorSizes = []abi.SectorSize{2048}
	power.ConsensusMinerMinPower = big.NewInt(2048)
}

func testGeneration(t testing.TB, n int, msgs int) {
	g, err := NewGenerator()
	if err != nil {
		t.Fatalf("%+v", err)
	}

	g.msgsPerBlock = msgs

	for i := 0; i < n; i++ {
		mts, err := g.NextTipSet()
		if err != nil {
			t.Fatalf("error at H:%d, %+v", i, err)
		}
		_ = mts
	}
}

func TestChainGeneration(t *testing.T) {
	testGeneration(t, 10, 20)
}

func BenchmarkChainGeneration(b *testing.B) {
	b.Run("0-messages", func(b *testing.B) {
		testGeneration(b, b.N, 0)
	})

	b.Run("10-messages", func(b *testing.B) {
		testGeneration(b, b.N, 10)
	})

	b.Run("100-messages", func(b *testing.B) {
		testGeneration(b, b.N, 100)
	})

	b.Run("1000-messages", func(b *testing.B) {
		testGeneration(b, b.N, 1000)
	})
}
