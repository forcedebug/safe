package signer

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/multi-party-sig/common/round"
	"github.com/MixinNetwork/multi-party-sig/pkg/party"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/signer/protocol"
	"github.com/MixinNetwork/trusted-group/mtg"
	"github.com/fox-one/mixin-sdk-go"
	"github.com/gofrs/uuid"
	"github.com/shopspring/decimal"
)

type Node struct {
	id        party.ID
	members   party.IDSlice
	threshold int

	conf       *Configuration
	group      *mtg.Group
	network    Network
	aesKey     [32]byte
	mutex      *sync.Mutex
	sessions   map[string]chan *protocol.Message
	operations map[string]bool
	store      *SQLite3Store

	keeper *mtg.Configuration
	mixin  *mixin.Client
}

func NewNode(store *SQLite3Store, group *mtg.Group, network Network, conf *Configuration, keeper *mtg.Configuration, mixin *mixin.Client) *Node {
	node := &Node{
		id:         party.ID(conf.MTG.App.ClientId),
		threshold:  conf.Threshold,
		conf:       conf,
		group:      group,
		network:    network,
		mutex:      new(sync.Mutex),
		sessions:   make(map[string]chan *protocol.Message),
		operations: make(map[string]bool),
		store:      store,
		keeper:     keeper,
		mixin:      mixin,
	}
	node.aesKey = common.ECDHEd25519(conf.SharedKey, conf.KeeperPublicKey)

	for _, id := range conf.MTG.Genesis.Members {
		node.members = append(node.members, party.ID(id))
	}
	if conf.MTG.Genesis.Threshold != len(node.members) {
		panic(fmt.Errorf("%d/%d", conf.MTG.Genesis.Threshold, len(node.members)))
	}

	return node
}

func (node *Node) Boot(ctx context.Context) {
	go node.loopInitialSessions(ctx)
	go node.loopPendingSessions(ctx)
	go node.acceptIncomingMessages(ctx)
	logger.Printf("node.Boot(%s, %d)", node.id, node.Index())
}

func (node *Node) loopInitialSessions(ctx context.Context) {
	for {
		time.Sleep(300 * time.Millisecond)
		synced, err := node.synced(ctx)
		if err != nil || !synced {
			logger.Printf("group.Synced(%s) => %t %v", node.group.GenesisId(), synced, err)
			time.Sleep(3 * time.Second)
			continue
		}
		sessions, err := node.store.ListSessions(ctx, common.RequestStateInitial, runtime.NumCPU())
		if err != nil {
			panic(err)
		}

		results := make([]<-chan error, len(sessions))
		for i, op := range sessions {
			results[i] = node.queueOperation(ctx, op)
		}
		for _, res := range results {
			if res == nil {
				continue
			}
			if err := <-res; err != nil {
				panic(err)
			}
		}
	}
}

func (node *Node) loopPendingSessions(ctx context.Context) {
	for {
		time.Sleep(300 * time.Millisecond)
		synced, err := node.synced(ctx)
		if err != nil || !synced {
			logger.Printf("group.Synced(%s) => %t %v", node.group.GenesisId(), synced, err)
			time.Sleep(3 * time.Second)
			continue
		}
		sessions, err := node.store.ListSessions(ctx, common.RequestStatePending, 64)
		if err != nil {
			panic(err)
		}

		for _, op := range sessions {
			switch op.Type {
			case common.OperationTypeKeygenInput:
				op.Extra = common.DecodeHexOrPanic(op.Public)
			case common.OperationTypeSignInput:
			default:
				panic(op.Id)
			}
			err := node.sendSignerResultTransaction(ctx, op)
			logger.Printf("node.sendSignerResultTransaction(%v) => %v", op, err)
			if err != nil {
				break
			}
			err = node.store.MarkSessionDone(ctx, op.Id)
			logger.Printf("node.MarkSessionDone(%v) => %v", op, err)
			if err != nil {
				break
			}
		}
	}
}

func (node *Node) Index() int {
	index := node.findMember(string(node.id))
	if index < 0 {
		panic(node.id)
	}
	return index
}

func (node *Node) findMember(m string) int {
	for i, id := range node.members {
		if m == string(id) {
			return i
		}
	}
	return -1
}

func (node *Node) synced(ctx context.Context) (bool, error) {
	if common.CheckTestEnvironment(ctx) {
		return true, nil
	}
	// TODO all nodes send group timestamp to others, and not synced
	// if one of them has a big difference
	return node.group.Synced()
}

func (node *Node) acceptIncomingMessages(ctx context.Context) {
	for {
		peer, msb, err := node.network.ReceiveMessage(ctx)
		logger.Debugf("network.ReceiveMessage() => %s %x %v", peer, msb, err)
		if err != nil {
			panic(err)
		}
		sessionId, msg, err := unmarshalSessionMessage(msb)
		logger.Verbosef("node.acceptIncomingMessages(%x, %d) => %s %x", sessionId, msg.RoundNumber, peer, msg.SSID)
		if err != nil {
			continue
		}
		if msg.SSID == nil {
			continue
		}
		if msg.From != party.ID(peer) {
			continue
		}
		if !msg.IsFor(node.id) {
			continue
		}
		node.getSession(sessionId) <- msg
		if msg.RoundNumber != MPCFirstMessageRound {
			continue
		}

		id := uuid.Must(uuid.FromBytes(sessionId))
		r, err := node.store.ReadSession(ctx, id.String())
		if err != nil {
			panic(err)
		}
		if r == nil {
			continue
		}
		if r.State == common.RequestStateInitial {
			node.queueOperation(ctx, &common.Operation{
				Id:     r.Id,
				Type:   r.Operation,
				Curve:  r.Curve,
				Public: r.Public,
				Extra:  common.DecodeHexOrPanic(r.Extra),
			})
		} else {
			rm := &protocol.Message{SSID: sessionId, From: node.id, To: party.ID(peer)}
			rmb := marshalSessionMessage(sessionId, rm)
			node.network.QueueMessage(ctx, peer, rmb)
		}
	}
}

func (node *Node) queueOperation(ctx context.Context, op *common.Operation) <-chan error {
	node.mutex.Lock()
	defer node.mutex.Unlock()

	if node.operations[op.Id] {
		return nil
	}
	node.operations[op.Id] = true

	res := make(chan error)
	go func() { res <- node.startOperation(ctx, op) }()
	return res
}

func (node *Node) handlerLoop(ctx context.Context, start round.Session, sessionId []byte, timeout time.Duration) (any, error) {
	logger.Printf("node.handlerLoop(%x) => %x", sessionId, start.SSID())
	h, err := protocol.NewMultiHandler(start)
	if err != nil {
		return nil, err
	}
	incoming := node.getSession(sessionId)

	for {
		select {
		case msg, ok := <-h.Listen():
			if !ok {
				return h.Result()
			}
			msb := marshalSessionMessage(sessionId, msg)
			for _, id := range node.members {
				if !msg.IsFor(id) {
					continue
				}
				err := node.network.QueueMessage(ctx, string(id), msb)
				logger.Verbosef("network.QueueMessage(%x, %d) => %s %v", sessionId, msg.RoundNumber, id, err)
			}
		case msg := <-incoming:
			logger.Verbosef("network.incoming %x %d %s", sessionId, msg.RoundNumber, msg.From)
			if bytes.Equal(sessionId, msg.SSID) {
				return nil, fmt.Errorf("node.handlerLoop(%x) expired from %s", sessionId, msg.From)
			} else {
				h.Accept(msg)
			}
			logger.Verbosef("handler.Accept %x %d %s", sessionId, msg.RoundNumber, msg.From)
		case <-time.After(timeout):
			return nil, fmt.Errorf("node.handlerLoop(%x) timeout", sessionId)
		}
	}
}

func (node *Node) getSession(sessionId []byte) chan *protocol.Message {
	node.mutex.Lock()
	defer node.mutex.Unlock()

	sid := hex.EncodeToString(sessionId)
	session := node.sessions[sid]

	if session == nil {
		size := len(node.members) * len(node.members)
		session = make(chan *protocol.Message, size)
		node.sessions[sid] = session
	}
	return session
}

func marshalSessionMessage(sessionId []byte, msg *protocol.Message) []byte {
	if len(sessionId) > 32 {
		panic(hex.EncodeToString(sessionId))
	}
	msb := []byte{byte(len(sessionId))}
	msb = append(msb, sessionId...)
	return append(msb, common.MarshalPanic(msg)...)
}

func unmarshalSessionMessage(b []byte) ([]byte, *protocol.Message, error) {
	if len(b) < 16 {
		return nil, nil, fmt.Errorf("unmarshalSessionMessage(%x) short", b)
	}
	if len(b[1:]) <= int(b[0]) {
		return nil, nil, fmt.Errorf("unmarshalSessionMessage(%x) short", b)
	}
	sessionId := b[1 : 1+b[0]]
	var msg protocol.Message
	err := msg.UnmarshalBinary(b[1+b[0]:])
	return sessionId, &msg, err
}

func (node *Node) sendSignerResultTransaction(ctx context.Context, op *common.Operation) error {
	extra := common.AESEncrypt(node.aesKey[:], op.Encode(), op.Id)
	if len(extra) > 160 {
		panic(fmt.Errorf("node.sendSignerResultTransaction(%v) omitted %x", op, extra))
	}
	members := node.conf.MTG.Genesis.Members
	threshold := node.conf.MTG.Genesis.Threshold
	traceId := fmt.Sprintf("SESSION:%s:SIGNER:%s:RESULT", op.Id, string(node.id))
	traceId = mixin.UniqueConversationID(traceId, fmt.Sprintf("MTG:%v:%d", members, threshold))
	err := node.sendTransactionUntilSufficient(ctx, node.conf.AssetId, members, threshold, decimal.NewFromInt(1), extra, traceId)
	logger.Printf("node.sendSignerResultTransaction(%v) => %s %x %v", op, op.Id, extra, err)
	return err
}

func (node *Node) sendTransactionUntilSufficient(ctx context.Context, assetId string, receivers []string, threshold int, amount decimal.Decimal, memo []byte, traceId string) error {
	if common.CheckTestEnvironment(ctx) {
		out := &mtg.Output{Sender: string(node.id), AssetID: node.conf.AssetId}
		out.Memo = common.Base91Encode(memo)
		data, _ := json.Marshal(out)
		msg := common.MarshalPanic(&protocol.Message{Data: data})
		extra := append([]byte{16}, uuid.Nil.Bytes()...)
		extra = append(extra, msg...)
		return node.network.BroadcastMessage(ctx, extra)
	}

	return common.SendTransactionUntilSufficient(ctx, node.mixin, assetId, receivers, threshold, amount, common.Base91Encode(memo), traceId, node.conf.MTG.App.PIN)
}