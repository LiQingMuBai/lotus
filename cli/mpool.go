package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"golang.org/x/xerrors"
	"gopkg.in/urfave/cli.v2"

	"github.com/filecoin-project/go-address"

	"github.com/filecoin-project/lotus/chain/types"
)

var mpoolCmd = &cli.Command{
	Name:  "mpool",
	Usage: "Manage message pool",
	Subcommands: []*cli.Command{
		mpoolPending,
		mpoolSub,
		mpoolStat,
	},
}

var mpoolPending = &cli.Command{
	Name:  "pending",
	Usage: "Get pending messages",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		msgs, err := api.MpoolPending(ctx, types.EmptyTSK)
		if err != nil {
			return err
		}

		for _, msg := range msgs {
			out, err := json.MarshalIndent(msg, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
		}

		return nil
	},
}

var mpoolSub = &cli.Command{
	Name:  "sub",
	Usage: "Subscibe to mpool changes",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		sub, err := api.MpoolSub(ctx)
		if err != nil {
			return err
		}

		for {
			select {
			case update := <-sub:
				out, err := json.MarshalIndent(update, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
			case <-ctx.Done():
				return nil
			}
		}
	},
}

type statBucket struct {
	msgs map[uint64]*types.SignedMessage
}
type mpStat struct {
	addr              string
	past, cur, future uint64
}

var mpoolStat = &cli.Command{
	Name:  "stat",
	Usage: "print mempool stats",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		ts, err := api.ChainHead(ctx)
		if err != nil {
			return xerrors.Errorf("getting chain head: %w", err)
		}

		msgs, err := api.MpoolPending(ctx, types.EmptyTSK)
		if err != nil {
			return err
		}

		buckets := map[address.Address]*statBucket{}

		for _, v := range msgs {
			bkt, ok := buckets[v.Message.From]
			if !ok {
				bkt = &statBucket{
					msgs: map[uint64]*types.SignedMessage{},
				}
				buckets[v.Message.From] = bkt
			}

			bkt.msgs[v.Message.Nonce] = v
		}

		var out []mpStat

		for a, bkt := range buckets {
			act, err := api.StateGetActor(ctx, a, ts.Key())
			if err != nil {
				fmt.Printf("%s, err: %s\n", a, err)
				continue
			}

			cur := act.Nonce
			for {
				_, ok := bkt.msgs[cur]
				if !ok {
					break
				}
				cur++
			}

			past := uint64(0)
			future := uint64(0)
			for _, m := range bkt.msgs {
				if m.Message.Nonce < act.Nonce {
					past++
				}
				if m.Message.Nonce > cur {
					future++
				}
			}

			out = append(out, mpStat{
				addr:   a.String(),
				past:   past,
				cur:    cur - act.Nonce,
				future: future,
			})
		}

		sort.Slice(out, func(i, j int) bool {
			return out[i].addr < out[j].addr
		})

		for _, stat := range out {
			fmt.Printf("%s, past: %d, cur: %d, future: %d\n", stat.addr, stat.past, stat.cur, stat.future)
		}

		return nil
	},
}
