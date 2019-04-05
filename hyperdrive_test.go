package hyperdrive_test

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/renproject/hyperdrive/block"
	"github.com/renproject/hyperdrive/consensus"
	"github.com/renproject/hyperdrive/replica"
	"github.com/renproject/hyperdrive/shard"
	"github.com/renproject/hyperdrive/sig"
	"github.com/renproject/hyperdrive/sig/ecdsa"
	"github.com/renproject/hyperdrive/testutils"
	"github.com/renproject/hyperdrive/tx"
	co "github.com/republicprotocol/co-go"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/renproject/hyperdrive"
)

var _ = Describe("Hyperdrive", func() {

	table := []struct {
		numHyperdrives int
		maxHeight      block.Height
	}{
		{1, 100},
		{2, 100},
		{4, 100},
		{8, 50},
		{16, 50},
		{32, 30},
		{64, 15},
		{128, 7},
		{256, 2},

		// CircleCI times out on the following configurations
		// {512},
		// {1024},
	}

	for _, entry := range table {
		entry := entry

		Context(fmt.Sprintf("when reaching consensus on a shard with %v replicas", entry.numHyperdrives), func() {
			It("should commit blocks", func() {
				done := make(chan struct{})
				ipChans := make([]chan Object, entry.numHyperdrives)
				signatories := make(sig.Signatories, entry.numHyperdrives)
				signers := make([]sig.SignerVerifier, entry.numHyperdrives)

				for i := 0; i < entry.numHyperdrives; i++ {
					var err error
					ipChans[i] = make(chan Object, entry.numHyperdrives*entry.numHyperdrives)
					signers[i], err = ecdsa.NewFromRandom()
					signatories[i] = signers[i].Signatory()
					Expect(err).ShouldNot(HaveOccurred())
				}

				shardHash := testutils.RandomHash()
				for i := 0; i < entry.numHyperdrives; i++ {
					blockchain := block.NewBlockchain()
					shard := shard.Shard{
						Hash:        shardHash,
						Signatories: make(sig.Signatories, entry.numHyperdrives),
					}
					copy(shard.Signatories[:], signatories[:])
					ipChans[i] <- ShardObject{shard, blockchain, tx.FIFOPool()}
				}

				co.ParForAll(entry.numHyperdrives, func(i int) {
					if i == 0 {
						time.Sleep(time.Second)
					}
					runHyperdrive(i, NewMockDispatcher(i, ipChans, done), signers[i], ipChans[i], done, entry.maxHeight)
				})
			})
		})

	}
})

type mockDispatcher struct {
	index int

	dups     map[string]bool
	channels []chan Object
	done     chan struct{}
}

func NewMockDispatcher(i int, channels []chan Object, done chan struct{}) *mockDispatcher {
	return &mockDispatcher{
		index: i,

		dups:     map[string]bool{},
		channels: channels,

		done: done,
	}
}

func (mockDispatcher *mockDispatcher) Dispatch(shardHash sig.Hash, action consensus.Action) {

	// De-duplicate
	height := block.Height(0)
	round := block.Round(0)
	switch action := action.(type) {
	case consensus.Propose:
		height = action.Height
		round = action.Round
	case consensus.SignedPreVote:
		height = action.Height
		round = action.Round
	case consensus.SignedPreCommit:
		height = action.Polka.Height
		round = action.Polka.Round
	case consensus.Commit:
		height = action.Polka.Height
		round = action.Polka.Round
	default:
		panic(fmt.Errorf("unexpected action type %T", action))
	}

	key := fmt.Sprintf("Key(Shard=%v,Height=%v,Round=%v,Action=%T)", shardHash, height, round, action)
	if dup := mockDispatcher.dups[key]; dup {
		return
	}
	mockDispatcher.dups[key] = true

	go func() {
		if mockDispatcher.index > len(mockDispatcher.channels)-len(mockDispatcher.channels)/3 {
			return
		}
		for i := range mockDispatcher.channels {
			select {
			case <-mockDispatcher.done:
				return
			case mockDispatcher.channels[i] <- ActionObject{shardHash, action}:
			}
		}
	}()
}

type Object interface {
	IsObject()
}

type ActionObject struct {
	shardHash sig.Hash
	action    consensus.Action
}

func (ActionObject) IsObject() {}

type ShardObject struct {
	shard      shard.Shard
	blockchain block.Blockchain
	pool       tx.Pool
}

func (ShardObject) IsObject() {}

func runHyperdrive(index int, dispatcher replica.Dispatcher, signer sig.SignerVerifier, inputCh chan Object, done chan struct{}, maxHeight block.Height) {
	h := New(signer, dispatcher)

	var currentBlock *block.SignedBlock

	for {
		select {
		case <-done:
			return
		case input := <-inputCh:
			switch input := input.(type) {
			case ShardObject:
				h.AcceptShard(input.shard, input.blockchain, input.pool)
			case ActionObject:
				switch action := input.action.(type) {
				case consensus.Propose:
					h.AcceptPropose(input.shardHash, action.SignedBlock)
				case consensus.SignedPreVote:
					h.AcceptPreVote(input.shardHash, action.SignedPreVote)
				case consensus.SignedPreCommit:
					h.AcceptPreCommit(input.shardHash, action.SignedPreCommit)
				case consensus.Commit:
					if currentBlock == nil || action.Polka.Block.Height > currentBlock.Height {
						if currentBlock != nil {
							Expect(currentBlock.Height).To(Equal(action.Polka.Block.Height - 1))
							Expect(currentBlock.Header.Equal(action.Polka.Block.ParentHeader)).To(Equal(true))
						}
						if index == 0 {
							fmt.Printf("%v\n", *action.Polka.Block)
						}
						currentBlock = action.Polka.Block
						if currentBlock.Height == maxHeight {
							return
						}
					}
				default:
				}
			}
		}
	}
}

func rand32Byte() [32]byte {
	key := make([]byte, 32)

	rand.Read(key)
	b := [32]byte{}
	copy(b[:], key[:])
	return b
}