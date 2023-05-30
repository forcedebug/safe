package signer

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MixinNetwork/mixin/crypto"
	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/multi-party-sig/pkg/math/curve"
	"github.com/MixinNetwork/multi-party-sig/pkg/party"
	"github.com/MixinNetwork/multi-party-sig/protocols/cmp"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/signer/protocol"
	"github.com/MixinNetwork/trusted-group/mtg"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/fox-one/mixin-sdk-go"
	"github.com/gofrs/uuid"
	"github.com/pelletier/go-toml"
	"github.com/test-go/testify/require"
)

func TestPrepare(require *require.Assertions) (context.Context, []*Node) {
	logger.SetLevel(logger.VERBOSE)
	ctx := context.Background()
	ctx = common.EnableTestEnvironment(ctx)

	nodes := make([]*Node, 4)
	for i := 0; i < 4; i++ {
		dir := fmt.Sprintf("safe-signer-test-%d", i)
		root, err := os.MkdirTemp("", dir)
		require.Nil(err)
		nodes[i] = testBuildNode(ctx, require, root, i)
	}

	network := newTestNetwork(nodes[0].members)
	for i := 0; i < 4; i++ {
		nodes[i].network = network
		ctx = context.WithValue(ctx, "party", string(nodes[i].id))
		go network.mtgLoop(ctx, nodes[i])
		go nodes[i].loopInitialSessions(ctx)
		go nodes[i].loopPendingSessions(ctx)
		go nodes[i].acceptIncomingMessages(ctx)
	}

	return ctx, nodes
}

func TestCMPPrepareKeys(ctx context.Context, require *require.Assertions, nodes []*Node, crv byte) string {
	const public = "02bf0a7fa4b7905a0de5ab60a5322529e1a591ddd1ee53df82e751e8adb4bed08c"
	sid := mixin.UniqueConversationID("prepare", public)
	for _, node := range nodes {
		parts := strings.Split(testCMPKeys[node.id], ";")
		pub, share := parts[0], parts[1]
		sb, _ := hex.DecodeString(share)
		require.Equal(public, pub)

		op := &common.Operation{Id: sid, Curve: crv, Type: common.OperationTypeKeygenInput}
		err := node.store.WriteSessionIfNotExist(ctx, op, crypto.NewHash([]byte(sid)), 0, time.Now().UTC())
		require.Nil(err)
		err = node.store.WriteKeyIfNotExists(ctx, op.Id, crv, pub, sb)
		require.Nil(err)

		conf := cmp.EmptyConfig(curve.Secp256k1{})
		conf.UnmarshalBinary(sb)

		key, _ := hex.DecodeString(public)
		parentFP := []byte{0x00, 0x00, 0x00, 0x00}
		version := []byte{0x04, 0x88, 0xb2, 0x1e}
		extPub := hdkeychain.NewExtendedKey(version, key, conf.ChainKey, parentFP, 0, 0, false)
		require.Equal("xpub661MyMwAqRbcGz6ujRJnzrBvWrkz2NdNzYc3ZGBMVPmPBTHomqTiX5RrcTZVYZR2jM75oBU1UFssyMFqHV6GDsreibF2tPMbCcSPnTfqwhM", extPub.String())
		ecPub, err := extPub.ECPubKey()
		require.Nil(err)
		require.Equal(key, ecPub.SerializeCompressed())

		for i := uint32(0); i < 3; i++ {
			conf, err = conf.DeriveBIP32(i)
			require.Nil(err)
			spb := common.MarshalPanic(conf.PublicPoint())

			extPub, err = extPub.Derive(i)
			require.Nil(err)
			ecPub, _ = extPub.ECPubKey()
			bpb := ecPub.SerializeCompressed()

			require.Equal(bpb, spb)
			require.Equal([]byte(conf.ChainKey), extPub.ChainCode())
		}
	}
	return public
}

func testCMPSign(ctx context.Context, require *require.Assertions, nodes []*Node, public string, msg []byte, crv byte) []byte {
	node := nodes[0]
	sid := mixin.UniqueConversationID("sign", hex.EncodeToString(msg))
	fingerPath := append(common.Fingerprint(public), []byte{0, 0, 0, 0}...)
	sop := &common.Operation{
		Type:   common.OperationTypeSignInput,
		Id:     sid,
		Curve:  crv,
		Public: hex.EncodeToString(fingerPath),
		Extra:  msg,
	}
	memo := mtg.EncodeMixinExtra("", sid, string(node.encryptOperation(sop)))
	out := &mtg.Output{
		AssetID:         node.conf.KeeperAssetId,
		Memo:            memo,
		TransactionHash: crypto.NewHash([]byte(sop.Id)),
	}
	op := TestCMPProcessOutput(ctx, require, nodes, out, sid)

	require.Equal(common.OperationTypeSignOutput, int(op.Type))
	require.Equal(sid, op.Id)
	require.Equal(crv, op.Curve)
	require.Len(op.Public, 66)
	return op.Extra
}

func TestCMPProcessOutput(ctx context.Context, require *require.Assertions, nodes []*Node, out *mtg.Output, sessionId string) *common.Operation {
	network := nodes[0].network.(*testNetwork)
	for i := 0; i < 4; i++ {
		data, _ := json.Marshal(out)
		network.mtgChannels[nodes[i].id] <- data
	}

	var op *common.Operation
	for _, node := range nodes {
		op = testWaitOperation(ctx, node, sessionId)
	}
	return op
}

func testBuildNode(ctx context.Context, require *require.Assertions, root string, i int) *Node {
	f, _ := os.ReadFile("../config/example.toml")
	var conf struct {
		Signer *Configuration `toml:"signer"`
		Keeper struct {
			MTG *mtg.Configuration `toml:"mtg"`
		} `toml:"keeper"`
	}
	err := toml.Unmarshal(f, &conf)
	require.Nil(err)

	conf.Signer.StoreDir = root
	conf.Signer.MTG.App.ClientId = conf.Signer.MTG.Genesis.Members[i]

	if !strings.HasPrefix(conf.Signer.StoreDir, "/tmp/") {
		panic(root)
	}
	kd, err := OpenSQLite3Store(conf.Signer.StoreDir + "/mpc.sqlite3")
	require.Nil(err)

	node := NewNode(kd, nil, nil, conf.Signer, conf.Keeper.MTG, nil)
	return node
}

func testWaitOperation(ctx context.Context, node *Node, sessionId string) *common.Operation {
	timeout := time.Now().Add(time.Minute * 4)
	for ; time.Now().Before(timeout); time.Sleep(3 * time.Second) {
		val, err := node.store.ReadProperty(ctx, "KEEPER:"+sessionId)
		if err != nil {
			panic(err)
		}
		if val == "" {
			continue
		}
		b, _ := hex.DecodeString(val)
		b = common.AESDecrypt(node.aesKey[:], b)
		op, _ := common.DecodeOperation(b)
		if op != nil {
			return op
		}
	}
	return nil
}

type testNetwork struct {
	parties        party.IDSlice
	listenChannels map[party.ID]chan []byte
	mtgChannels    map[party.ID]chan []byte
	mtx            sync.Mutex
}

func newTestNetwork(parties party.IDSlice) *testNetwork {
	n := &testNetwork{
		parties:        parties,
		listenChannels: make(map[party.ID]chan []byte, 2*len(parties)),
		mtgChannels:    make(map[party.ID]chan []byte, 2*len(parties)),
	}
	N := len(n.parties)
	for _, id := range n.parties {
		n.listenChannels[id] = make(chan []byte, N*N)
		n.mtgChannels[id] = make(chan []byte, N*N)
	}
	return n
}

func (n *testNetwork) mtgLoop(ctx context.Context, node *Node) {
	filter := make(map[string]bool)
	loop := n.mtgChannels[node.id]
	for mob := range loop {
		k := hex.EncodeToString(mob)
		if filter[k] {
			continue
		}
		var out mtg.Output
		json.Unmarshal(mob, &out)
		node.ProcessOutput(ctx, &out)
		filter[k] = true
	}
}

func (n *testNetwork) ReceiveMessage(ctx context.Context) (string, []byte, error) {
	id := ctx.Value("party").(string)
	msb := <-n.listen(party.ID(id))
	_, msg, _ := unmarshalSessionMessage(msb)
	peer := string(msg.From)
	return peer, msb, nil
}

func (n *testNetwork) QueueMessage(ctx context.Context, receiver string, b []byte) error {
	sessionId, msg, err := unmarshalSessionMessage(b)
	if err != nil {
		return err
	}
	n.Send(sessionId, msg)
	return nil
}

func (n *testNetwork) BroadcastMessage(ctx context.Context, b []byte) error {
	for _, id := range n.parties {
		n.QueueMessage(ctx, string(id), b)
	}
	return nil
}

func (n *testNetwork) Send(sessionId []byte, msg *protocol.Message) {
	if bytes.Equal(sessionId, uuid.Nil.Bytes()) {
		for _, c := range n.mtgChannels {
			c <- msg.Data
		}
	} else {
		for _, id := range n.parties {
			if !msg.IsFor(id) {
				continue
			}
			n.listen(id) <- marshalSessionMessage(sessionId, msg)
		}
	}
}

func (n *testNetwork) listen(id party.ID) chan []byte {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	return n.listenChannels[id]
}

var testCMPKeys = map[party.ID]string{
	"member-id-0": "02bf0a7fa4b7905a0de5ab60a5322529e1a591ddd1ee53df82e751e8adb4bed08c;a96249446b6d656d6265722d69642d30695468726573686f6c64026545434453415820d2c3785b9befae6fd8dd0dcf08ee21aed1ca3ef69bb0e8fe4f6533e2f26eb2d067456c47616d616c5820036602c3d8c9f7b093b6a7427dfcab8fd278b55c30660571e28efad6b24c033a61505880fd37ff1b459e9cfc3d815e16e009050eeba3f450ac5e72b8fe3ee481ab7a71bb957a463eb7afebb0ff17ccc4fc2d7200773e75a0b286a45183f1869b2010167b5e67d8803490d77a8bb5f49150d8e4bba1b8104a5e6c6447a9ada9b21dc7ddda6886e058430c5958e7232b55001281c1abf64f83395c2053000c8489bb66c83761515880e5e69ba508950a4063916f865447f07834961fe9751351029d9b38eb69da89aedf60b9916da25b114b6154c27981ffc875643a9a0ea11baa71fdd13512fab70798e43ec61c0855baa26262ae383911eb1b242675680f86e8a022539e18fe5641bd5b6e06d6a24a20dc591f27b4c5761282e44e6ce490ea29161589b13da9a4cb635249445820b13663e6e4c5053c40bddee742ce959852f89acf55167a3ff0cc66bfa2f5a9f968436861696e4b65795820f555b08a9871213c0d52fee12e1bd365990b956880491b2b1a106f84584aa3a2665075626c696384a66249446b6d656d6265722d69642d30654543445341582102d16b97011927008338404f56d64f2d984fa70699c91f2c8185602721a284e0d767456c47616d616c582102c1f8227a5994d7bf2d996a10489c816845cfe94a424e0e3da4b49e659cbb20b8614e590100e3673176bcd18a5dcb6bb7a8027c71d26efb89205e521a9ee498ac24bbefb3d25b9e12429599fbedc864d2e164c2dfcc00f04014dcd883a2327b095c4f76508c8c06c2841fd273952509c32e7cf727476457d6799d52219fdb3d24e977b5bb1b60f0fc3537c6357777f194b25b8a35afdb04929637a20d9615b32212831c5134456178760251dc3298fbed5f73b0d09489667ce16e87355011bdb6310fef6f2f7020cf5466e7e672fdefffa951cd4207df12d0e8f5bbb5823fc315ae3227433e16086ef89651042b3cdf4028751cfc7c78bd460424cd1787bf69f095c65818a3b08214eeb1c7c37be11eac68d88aa0c0f33d14d7a0f5b5a8192266dbba12ff9d6153590100c4b5aed987e4e31098f21f3184572054acaff47f6003f27cc89529cb2b5de4903a35c54ec9db439391961f256fb64b7fe6fff2bfdeca8a5406f4e78a3bf4e293d0adc698b130d159d35bc48f63fead1170d8f26cd73aa2380ec9c1515ba9febe9153e08bec01f6efd6ac02268c68d58868fbb9b1c1f71788befc995bd0e82d35ab9f0ad6a8cf4ba6319cb6c39a109f99513b5b04fbae532da64583be66ed88bffa1572151e28af9defecea3bd0f2b862dd8820fcc9c5bef12f71309fa23cb10a4a8d5db604c8577e356c83388043bdb0a58279d9de53c214e1dea1e2ad5a47bead3bbd33e71bc763a086140819f308980fc457547afa4c62d4a2601ec24cc2b9615459010029a560c8f3aaea766c9f31e61fd665c65b6ca4e72d1374484a8000d6f57711df03a95fbe1c7ebdc9359b902f08bc48ce06a116be5dc38416298cae1481b19689244016038f7e74bbed78b80afc806fab2b86d7af4d0f57d99ff9f075b0ab654c1314d4444eb006e99ab543b51a701f5972693003703b8dd8e5ad37f8f33e9f09f4f029ea7d3310dc2c00b89c3bd32ce2427306131cacafcf8e399f49756c38012d1f614090d38a1412752c95b08414dc4f8d940526b593720fdbe9a1053e3c7a89c380118fcec394e5c8f38ee6db241d5181eee58f82950a9b4e37a954440994b8a81e839ce9610627f49fdd1a6ec98dd5a4641b7c58b0d06e434d54b7f47339a66249446b6d656d6265722d69642d3165454344534158210205911bbe040e9b1e4cb1276d34f339cf56e2688d7d0e6c1d67df37ea9adf303f67456c47616d616c582102debdda71b628a8e0b8919064c2ba40d1297271281c7b87a2b911fc0ec42787ed614e590100bd4cdd83f18803265b8158f87cbd448c3f329946f04a074cca9cfc5bc898d1b34c44bc69ba124b5921739df3a773aafc64871cfa11e0d466daffe638c9595f5f84980f4b35b571d32387edd2cb59b51cd11bb475ab42482248d788d4176911bc718c37a51eda81d2d73d8e1d0d4f185bea33804ba114a546f76d3e24dede18c2419251bc48e0446ba7714d3327bd12c3e62ae60b130813604ecc240992d8af586c8f9b32c1324779c18b9e1080acb2ccf70e83a09ed57c141762702ce2383161f9bdb6f0ffdbcf3c37df219f301db4da185d6e52e9060b6173dc01a78fc7bc0e0153383e515390d7992a6c828d37c0cbaa306e812a55714b9b212cbd19a8e5fd615359010016c7797854a64e2cd8b75550e6930fa74dd05d1524ab914e96ebe12c755f64afbd39387d4c358fe2a182d7f3fdf4b5a61416cfccf203e5369a358467d94580ba6b4cc98b5bf180101e8787d5e39984fc6235ad2526efb38ef3606654b80ea93c1c35937123c8a43b2cc04873cd6236126d8f17f18f576ce0250d2ae65edccec178729d89945bbf45776f6747b6ef09e3ecf06a4bf5701b847ca9bc3831ad3a62d329ed524a2517035e115e2dbe163102fc215aac49a92b13e2be282964681cef742e36d792d4793500390f38661a4fadfbdabc722fa4ce4db640dc628642706a85b47a7ade8381ddd664a661b23ecf49a89777adea493b6f4a9e68d18a407e4c6154590100287ceae05c8c8add2f575d26d457cee92d76dc643d1e2bb5f485d6e9b4d0180a6691f0ae9e7f4e8d8c6776d2d305acf173e5edaf17287d53148468fce7a2cc3cc7e5675b9d9ab043fcf8adef3698922e8736d781c40992ff836f5ed85cf23a8fc6da79b913845bfe3cabcc72bc1a672bca98b6c70f0aa53414efaf9b544c95a029d965d06c84b3364a4d53aed9462bc1b1da1590a61dbd25b2b437256b3ee6aeb3d662cd36c580e044435f0f4a0109c190091cacf5570d0eb54c5c056c9786f00eda835649dc80decdcbb391fa04ca370ecf86b103574e035b1f6efcab4f9f5aa89fc7711852b0daaf4ca35a72ba2d898929d9bd958b48daaf0e4af6a0eda693a66249446b6d656d6265722d69642d3265454344534158210230627dc5e3d64a959581e44ebc60b593b87517bb501ae8bcfdc7b4b21a2dda2f67456c47616d616c582102179e1b352d573edeca6919748f4b905a015febe7bac04b82885d7e210fceb3ef614e590100dce1b2620945fac45054e1d9f6de47427e85ecf0d6fbbbfc35d3784f5c281d96c434f99a2f784e7a76373680741869c183a7e09931445d78d2f1b602008cbcbd81864066e292dba5627b779d2547bb6451cde23b3748def6dbbc4611c588af1a9963f345140070270c979703646ba3a0b04a49c5a230767e3867431ecc7819f01eec83a6b64c5cd94528c9de143b98b0b3b51ced0702227a9405dfc38d82dde103e565737a17c915a84603c581d3007a1fa31f8e217e562f018a2e870622b3ed16f5205e9af7ea53be9fd29b95f4c3cbe340a2c119be0c5998fb929adfe59a64df1881562647a28bd8ddf2dcef7182895bf7eb2552b20e95e89d18fd02df7fc1615359010004e3ae6b395ce0bb2be593b90d508fbaf7f2953406423631b9dfdc042f043e6c48112dd6073d9d5b381b8e6c719eea5a1db7c85797dbcd1cb140e423ff3ad7dc792dd9177d648c67a3b5c31e3102fba1bf7211f21a9c9e7a719e87d35e1caea0c5bcf13214876c55188d06444c18aa7f8aad0b856ec3e1ec8c80f9db2b705e46874a6ae354c0b3689cf8e9b6ca2f381c9792a00dddce6e5549dae0da5bd85cabcfd4e436216dc1f4d57e45e3d204b2ee72d7f7d27c230045f2bbb418fa32dce4799dbbb6d4929a34cca24e9642ad8df4f8ec09f089f9a76bb024398704c4614c095b51210f8da55467d62f036248ed2c57a4d13ba43e44c3e8d9c65b3010aeaf61545901003f99e748e69cc8238c2150beb9b4d25d3b495db676d75d6e11abe138f9b7a52daf703c21e8546ca35bcf8ef87df77ee4bc096f34472f5deab69bf6be9d1d179e61bbab9b2fb6c041e6c733bd0b8d4a0e6fd94070452c946ec2b695ea9cc5e7fe519b049b250ef692a6be23ef508ed3c98957810c96fa2d4345b9f27debeb5b0e4b705e25f3cc484efb0487831f4b2efb2f4fa8c6473e960f34d57a2f9a0bfa791dc59e2def63712e17b50f5082f58d08b79581b61fa22b5dbf6b0b8c1e5142527f2576b0c81dff96ba02757ffb33b52efc3bfbb1775d2f282ba8cdcb91b20f6f4fc3d4f823ae4349acf4d2d7eafeac49c78beede2d8c70b6b208e9b4c2a859d4a66249446b6d656d6265722d69642d3365454344534158210371f3ab8aa11ebaf368eae875398791ecfac356fe35adb4ef9c727645ed311a9067456c47616d616c582102bb4a36f56b23af462bca403704e0ef456f3409155c5c059efc1d67cea119317b614e590100b0dc1900d8ee92e5ac175eed6b3afa742eaf3ca70d4a77848a55644daed3bdd04525d883aab2ec0aaa294d88e6b3edd421ffa8b627985a480f950b0ac4137f0c69e8968fca187394e4c17bb1804a33d84b2e56862592948aa569b0eb755047a248ba2b3b7284213ea9f667317a5098bab688d1ac7825324a9655c95f829b465781bc499a18f31f5d993acbd9b4efdb91e70084e9afe30e5a142a23ba6e0c069259b49a2a5dc5c6e593d27caf55a33d1dd34219c3da16e79958c2d11f0f5e753cde9ae67c4185e7fa6a695d2251033c38648bf9701d448ba9d4c939ce0c5194d71de5d092196f8f261b58cbe6648152b3262082f9b3527dbec8d4fa40048ae22961535901002a74d1ecf0a86489b4a1382998b58969ef6b5c81985d0587af746ca9893dac867725dbbc34dc7c4455a23a0f5d207294124c1e43ed0e1170f9ce3ea0bba9c18bf7a723cf8faa2c42134f21bf1215ac11e44cfd4233c401b54d2d968fa8cc8d9c80d52a38c328b08387829c9af71144371b9b95e3f0e192ad31cb55fe6a8e6d4f293476264e3fb866171b4a663d5f5bf1fafe2936547f39a46e5d44e6560ffc6bbfdbe82cf29e2389192cfece8d09492a6a036051676e69a7cc9c1a2f1ddf6c92b97516d9e5ab9293e751d120784664f20881715f12934e1116b341364b9191911b53c00ef98fc0bd667deae325446e28d902020e33451ccb76be94eb96bc03f96154590100019e725d7ffe4ce282a4c47e6559e86ca8aaa527a5db6a5f140a3948a1cdba6d891d06b239fe9c99f6d7c1db74ddeb05f8e0bec146aef7cce7133114e38c9f88810c30caa50cc6d3bc6dd01714e0a0d9bbf62bfd5ac301847ffae077ba2874b2586369c2e2345f26da526f3649b0749e60bf0d5f768009ee4d782ab1b489b25da1ce6dc434d201052bab5674c12852b082359f397278a8b339e924fb217348b383f6246247dcbce2b71b350f3cc94fb8c15cbfcbf5faa1951d2fa53f5a531cd4c877b9754d1d4ef0e817685c74e1dc093303fa4a89e910fb0bd8db1cd0b9e95660152a26fef680cdd5082d289da511ba4e278d4c69e8638cda4abe15454c0b77",
	"member-id-1": "02bf0a7fa4b7905a0de5ab60a5322529e1a591ddd1ee53df82e751e8adb4bed08c;a96249446b6d656d6265722d69642d31695468726573686f6c6402654543445341582080f03d8a5df13c452b0e86782d85b29a44176342772e207a29d8a72c4abbbbbb67456c47616d616c5820332e3fe4a572a3a54cf2f4b2e31b5bd0849f8fc3311fa5454f84391232fab42461505880defe6f37274269d7f9de2e3eb6aaef87a79122467be2faf16591015f3e9113fdf7c3a2641a3ec9590e1d4e54f733686f827090e19920db3c71a84dd42b3131383a1a8642d89257525c547eebd43f625c58243990d7682b78561cab57632e3a63a514d024f2da9df17febcd12523347039398f2832986ca3d9c4cb94f45b617fb61515880d951bad3655ae493c51b9183ef03d15f05a66773c88180fb11bdc6eeceb6bd8fdabfd6138f926219c7652bc67da1748595af44b284610af75b49d90a6c97cf88c2eb18886f6123e4be05b753f17ce63be6eb69ece843a260a1e0635eaf5df708f6a60afd1b96105baa16858871fcb7399f0dc48dfde3e790d7e6a544e8ebc067635249445820b13663e6e4c5053c40bddee742ce959852f89acf55167a3ff0cc66bfa2f5a9f968436861696e4b65795820f555b08a9871213c0d52fee12e1bd365990b956880491b2b1a106f84584aa3a2665075626c696384a66249446b6d656d6265722d69642d30654543445341582102d16b97011927008338404f56d64f2d984fa70699c91f2c8185602721a284e0d767456c47616d616c582102c1f8227a5994d7bf2d996a10489c816845cfe94a424e0e3da4b49e659cbb20b8614e590100e3673176bcd18a5dcb6bb7a8027c71d26efb89205e521a9ee498ac24bbefb3d25b9e12429599fbedc864d2e164c2dfcc00f04014dcd883a2327b095c4f76508c8c06c2841fd273952509c32e7cf727476457d6799d52219fdb3d24e977b5bb1b60f0fc3537c6357777f194b25b8a35afdb04929637a20d9615b32212831c5134456178760251dc3298fbed5f73b0d09489667ce16e87355011bdb6310fef6f2f7020cf5466e7e672fdefffa951cd4207df12d0e8f5bbb5823fc315ae3227433e16086ef89651042b3cdf4028751cfc7c78bd460424cd1787bf69f095c65818a3b08214eeb1c7c37be11eac68d88aa0c0f33d14d7a0f5b5a8192266dbba12ff9d6153590100c4b5aed987e4e31098f21f3184572054acaff47f6003f27cc89529cb2b5de4903a35c54ec9db439391961f256fb64b7fe6fff2bfdeca8a5406f4e78a3bf4e293d0adc698b130d159d35bc48f63fead1170d8f26cd73aa2380ec9c1515ba9febe9153e08bec01f6efd6ac02268c68d58868fbb9b1c1f71788befc995bd0e82d35ab9f0ad6a8cf4ba6319cb6c39a109f99513b5b04fbae532da64583be66ed88bffa1572151e28af9defecea3bd0f2b862dd8820fcc9c5bef12f71309fa23cb10a4a8d5db604c8577e356c83388043bdb0a58279d9de53c214e1dea1e2ad5a47bead3bbd33e71bc763a086140819f308980fc457547afa4c62d4a2601ec24cc2b9615459010029a560c8f3aaea766c9f31e61fd665c65b6ca4e72d1374484a8000d6f57711df03a95fbe1c7ebdc9359b902f08bc48ce06a116be5dc38416298cae1481b19689244016038f7e74bbed78b80afc806fab2b86d7af4d0f57d99ff9f075b0ab654c1314d4444eb006e99ab543b51a701f5972693003703b8dd8e5ad37f8f33e9f09f4f029ea7d3310dc2c00b89c3bd32ce2427306131cacafcf8e399f49756c38012d1f614090d38a1412752c95b08414dc4f8d940526b593720fdbe9a1053e3c7a89c380118fcec394e5c8f38ee6db241d5181eee58f82950a9b4e37a954440994b8a81e839ce9610627f49fdd1a6ec98dd5a4641b7c58b0d06e434d54b7f47339a66249446b6d656d6265722d69642d3165454344534158210205911bbe040e9b1e4cb1276d34f339cf56e2688d7d0e6c1d67df37ea9adf303f67456c47616d616c582102debdda71b628a8e0b8919064c2ba40d1297271281c7b87a2b911fc0ec42787ed614e590100bd4cdd83f18803265b8158f87cbd448c3f329946f04a074cca9cfc5bc898d1b34c44bc69ba124b5921739df3a773aafc64871cfa11e0d466daffe638c9595f5f84980f4b35b571d32387edd2cb59b51cd11bb475ab42482248d788d4176911bc718c37a51eda81d2d73d8e1d0d4f185bea33804ba114a546f76d3e24dede18c2419251bc48e0446ba7714d3327bd12c3e62ae60b130813604ecc240992d8af586c8f9b32c1324779c18b9e1080acb2ccf70e83a09ed57c141762702ce2383161f9bdb6f0ffdbcf3c37df219f301db4da185d6e52e9060b6173dc01a78fc7bc0e0153383e515390d7992a6c828d37c0cbaa306e812a55714b9b212cbd19a8e5fd615359010016c7797854a64e2cd8b75550e6930fa74dd05d1524ab914e96ebe12c755f64afbd39387d4c358fe2a182d7f3fdf4b5a61416cfccf203e5369a358467d94580ba6b4cc98b5bf180101e8787d5e39984fc6235ad2526efb38ef3606654b80ea93c1c35937123c8a43b2cc04873cd6236126d8f17f18f576ce0250d2ae65edccec178729d89945bbf45776f6747b6ef09e3ecf06a4bf5701b847ca9bc3831ad3a62d329ed524a2517035e115e2dbe163102fc215aac49a92b13e2be282964681cef742e36d792d4793500390f38661a4fadfbdabc722fa4ce4db640dc628642706a85b47a7ade8381ddd664a661b23ecf49a89777adea493b6f4a9e68d18a407e4c6154590100287ceae05c8c8add2f575d26d457cee92d76dc643d1e2bb5f485d6e9b4d0180a6691f0ae9e7f4e8d8c6776d2d305acf173e5edaf17287d53148468fce7a2cc3cc7e5675b9d9ab043fcf8adef3698922e8736d781c40992ff836f5ed85cf23a8fc6da79b913845bfe3cabcc72bc1a672bca98b6c70f0aa53414efaf9b544c95a029d965d06c84b3364a4d53aed9462bc1b1da1590a61dbd25b2b437256b3ee6aeb3d662cd36c580e044435f0f4a0109c190091cacf5570d0eb54c5c056c9786f00eda835649dc80decdcbb391fa04ca370ecf86b103574e035b1f6efcab4f9f5aa89fc7711852b0daaf4ca35a72ba2d898929d9bd958b48daaf0e4af6a0eda693a66249446b6d656d6265722d69642d3265454344534158210230627dc5e3d64a959581e44ebc60b593b87517bb501ae8bcfdc7b4b21a2dda2f67456c47616d616c582102179e1b352d573edeca6919748f4b905a015febe7bac04b82885d7e210fceb3ef614e590100dce1b2620945fac45054e1d9f6de47427e85ecf0d6fbbbfc35d3784f5c281d96c434f99a2f784e7a76373680741869c183a7e09931445d78d2f1b602008cbcbd81864066e292dba5627b779d2547bb6451cde23b3748def6dbbc4611c588af1a9963f345140070270c979703646ba3a0b04a49c5a230767e3867431ecc7819f01eec83a6b64c5cd94528c9de143b98b0b3b51ced0702227a9405dfc38d82dde103e565737a17c915a84603c581d3007a1fa31f8e217e562f018a2e870622b3ed16f5205e9af7ea53be9fd29b95f4c3cbe340a2c119be0c5998fb929adfe59a64df1881562647a28bd8ddf2dcef7182895bf7eb2552b20e95e89d18fd02df7fc1615359010004e3ae6b395ce0bb2be593b90d508fbaf7f2953406423631b9dfdc042f043e6c48112dd6073d9d5b381b8e6c719eea5a1db7c85797dbcd1cb140e423ff3ad7dc792dd9177d648c67a3b5c31e3102fba1bf7211f21a9c9e7a719e87d35e1caea0c5bcf13214876c55188d06444c18aa7f8aad0b856ec3e1ec8c80f9db2b705e46874a6ae354c0b3689cf8e9b6ca2f381c9792a00dddce6e5549dae0da5bd85cabcfd4e436216dc1f4d57e45e3d204b2ee72d7f7d27c230045f2bbb418fa32dce4799dbbb6d4929a34cca24e9642ad8df4f8ec09f089f9a76bb024398704c4614c095b51210f8da55467d62f036248ed2c57a4d13ba43e44c3e8d9c65b3010aeaf61545901003f99e748e69cc8238c2150beb9b4d25d3b495db676d75d6e11abe138f9b7a52daf703c21e8546ca35bcf8ef87df77ee4bc096f34472f5deab69bf6be9d1d179e61bbab9b2fb6c041e6c733bd0b8d4a0e6fd94070452c946ec2b695ea9cc5e7fe519b049b250ef692a6be23ef508ed3c98957810c96fa2d4345b9f27debeb5b0e4b705e25f3cc484efb0487831f4b2efb2f4fa8c6473e960f34d57a2f9a0bfa791dc59e2def63712e17b50f5082f58d08b79581b61fa22b5dbf6b0b8c1e5142527f2576b0c81dff96ba02757ffb33b52efc3bfbb1775d2f282ba8cdcb91b20f6f4fc3d4f823ae4349acf4d2d7eafeac49c78beede2d8c70b6b208e9b4c2a859d4a66249446b6d656d6265722d69642d3365454344534158210371f3ab8aa11ebaf368eae875398791ecfac356fe35adb4ef9c727645ed311a9067456c47616d616c582102bb4a36f56b23af462bca403704e0ef456f3409155c5c059efc1d67cea119317b614e590100b0dc1900d8ee92e5ac175eed6b3afa742eaf3ca70d4a77848a55644daed3bdd04525d883aab2ec0aaa294d88e6b3edd421ffa8b627985a480f950b0ac4137f0c69e8968fca187394e4c17bb1804a33d84b2e56862592948aa569b0eb755047a248ba2b3b7284213ea9f667317a5098bab688d1ac7825324a9655c95f829b465781bc499a18f31f5d993acbd9b4efdb91e70084e9afe30e5a142a23ba6e0c069259b49a2a5dc5c6e593d27caf55a33d1dd34219c3da16e79958c2d11f0f5e753cde9ae67c4185e7fa6a695d2251033c38648bf9701d448ba9d4c939ce0c5194d71de5d092196f8f261b58cbe6648152b3262082f9b3527dbec8d4fa40048ae22961535901002a74d1ecf0a86489b4a1382998b58969ef6b5c81985d0587af746ca9893dac867725dbbc34dc7c4455a23a0f5d207294124c1e43ed0e1170f9ce3ea0bba9c18bf7a723cf8faa2c42134f21bf1215ac11e44cfd4233c401b54d2d968fa8cc8d9c80d52a38c328b08387829c9af71144371b9b95e3f0e192ad31cb55fe6a8e6d4f293476264e3fb866171b4a663d5f5bf1fafe2936547f39a46e5d44e6560ffc6bbfdbe82cf29e2389192cfece8d09492a6a036051676e69a7cc9c1a2f1ddf6c92b97516d9e5ab9293e751d120784664f20881715f12934e1116b341364b9191911b53c00ef98fc0bd667deae325446e28d902020e33451ccb76be94eb96bc03f96154590100019e725d7ffe4ce282a4c47e6559e86ca8aaa527a5db6a5f140a3948a1cdba6d891d06b239fe9c99f6d7c1db74ddeb05f8e0bec146aef7cce7133114e38c9f88810c30caa50cc6d3bc6dd01714e0a0d9bbf62bfd5ac301847ffae077ba2874b2586369c2e2345f26da526f3649b0749e60bf0d5f768009ee4d782ab1b489b25da1ce6dc434d201052bab5674c12852b082359f397278a8b339e924fb217348b383f6246247dcbce2b71b350f3cc94fb8c15cbfcbf5faa1951d2fa53f5a531cd4c877b9754d1d4ef0e817685c74e1dc093303fa4a89e910fb0bd8db1cd0b9e95660152a26fef680cdd5082d289da511ba4e278d4c69e8638cda4abe15454c0b77",
	"member-id-2": "02bf0a7fa4b7905a0de5ab60a5322529e1a591ddd1ee53df82e751e8adb4bed08c;a96249446b6d656d6265722d69642d32695468726573686f6c64026545434453415820f6f534c4308d09ef0e90c087ac47c23763aba85bfc8fb284c1170218b63f140a67456c47616d616c58204be233ba60be43eb4c4fb0838c30e6ee706f0bd8bcb33936c17de5244cd6eaf761505880fc5240ec5d4a9786feef284fad957bb1b8df6a02802cab7fe6975515a0d525a6594d2e8b5dd9d891855e790794b85df7427966a45a83a411bc5151bbfc3eff3d278d44012b99cd8ecc2f6f9cdfb5c65cf27811d6c25f8add34311cd307043c7a8940a7f35621517dfabca0105df9493a770d27860b84a6745bb6f3f17245b26f61515880e01a1996c1e35e79a2c008490347e6eebd61232525a51e9a877ab018189ae39287881a9898b9fc276ec28ca4f0148fcbf05a18dd26a66f0822da1e85ff16f0a208009e3e67ee11949143a4614a7fb83c1f345f4d7c1191a9080a03e004a84d7a4f9d78d6fff74152b0c7033bbbd083ecf9b7b2bf54709e2e7df8d20aa97b48cf635249445820b13663e6e4c5053c40bddee742ce959852f89acf55167a3ff0cc66bfa2f5a9f968436861696e4b65795820f555b08a9871213c0d52fee12e1bd365990b956880491b2b1a106f84584aa3a2665075626c696384a66249446b6d656d6265722d69642d30654543445341582102d16b97011927008338404f56d64f2d984fa70699c91f2c8185602721a284e0d767456c47616d616c582102c1f8227a5994d7bf2d996a10489c816845cfe94a424e0e3da4b49e659cbb20b8614e590100e3673176bcd18a5dcb6bb7a8027c71d26efb89205e521a9ee498ac24bbefb3d25b9e12429599fbedc864d2e164c2dfcc00f04014dcd883a2327b095c4f76508c8c06c2841fd273952509c32e7cf727476457d6799d52219fdb3d24e977b5bb1b60f0fc3537c6357777f194b25b8a35afdb04929637a20d9615b32212831c5134456178760251dc3298fbed5f73b0d09489667ce16e87355011bdb6310fef6f2f7020cf5466e7e672fdefffa951cd4207df12d0e8f5bbb5823fc315ae3227433e16086ef89651042b3cdf4028751cfc7c78bd460424cd1787bf69f095c65818a3b08214eeb1c7c37be11eac68d88aa0c0f33d14d7a0f5b5a8192266dbba12ff9d6153590100c4b5aed987e4e31098f21f3184572054acaff47f6003f27cc89529cb2b5de4903a35c54ec9db439391961f256fb64b7fe6fff2bfdeca8a5406f4e78a3bf4e293d0adc698b130d159d35bc48f63fead1170d8f26cd73aa2380ec9c1515ba9febe9153e08bec01f6efd6ac02268c68d58868fbb9b1c1f71788befc995bd0e82d35ab9f0ad6a8cf4ba6319cb6c39a109f99513b5b04fbae532da64583be66ed88bffa1572151e28af9defecea3bd0f2b862dd8820fcc9c5bef12f71309fa23cb10a4a8d5db604c8577e356c83388043bdb0a58279d9de53c214e1dea1e2ad5a47bead3bbd33e71bc763a086140819f308980fc457547afa4c62d4a2601ec24cc2b9615459010029a560c8f3aaea766c9f31e61fd665c65b6ca4e72d1374484a8000d6f57711df03a95fbe1c7ebdc9359b902f08bc48ce06a116be5dc38416298cae1481b19689244016038f7e74bbed78b80afc806fab2b86d7af4d0f57d99ff9f075b0ab654c1314d4444eb006e99ab543b51a701f5972693003703b8dd8e5ad37f8f33e9f09f4f029ea7d3310dc2c00b89c3bd32ce2427306131cacafcf8e399f49756c38012d1f614090d38a1412752c95b08414dc4f8d940526b593720fdbe9a1053e3c7a89c380118fcec394e5c8f38ee6db241d5181eee58f82950a9b4e37a954440994b8a81e839ce9610627f49fdd1a6ec98dd5a4641b7c58b0d06e434d54b7f47339a66249446b6d656d6265722d69642d3165454344534158210205911bbe040e9b1e4cb1276d34f339cf56e2688d7d0e6c1d67df37ea9adf303f67456c47616d616c582102debdda71b628a8e0b8919064c2ba40d1297271281c7b87a2b911fc0ec42787ed614e590100bd4cdd83f18803265b8158f87cbd448c3f329946f04a074cca9cfc5bc898d1b34c44bc69ba124b5921739df3a773aafc64871cfa11e0d466daffe638c9595f5f84980f4b35b571d32387edd2cb59b51cd11bb475ab42482248d788d4176911bc718c37a51eda81d2d73d8e1d0d4f185bea33804ba114a546f76d3e24dede18c2419251bc48e0446ba7714d3327bd12c3e62ae60b130813604ecc240992d8af586c8f9b32c1324779c18b9e1080acb2ccf70e83a09ed57c141762702ce2383161f9bdb6f0ffdbcf3c37df219f301db4da185d6e52e9060b6173dc01a78fc7bc0e0153383e515390d7992a6c828d37c0cbaa306e812a55714b9b212cbd19a8e5fd615359010016c7797854a64e2cd8b75550e6930fa74dd05d1524ab914e96ebe12c755f64afbd39387d4c358fe2a182d7f3fdf4b5a61416cfccf203e5369a358467d94580ba6b4cc98b5bf180101e8787d5e39984fc6235ad2526efb38ef3606654b80ea93c1c35937123c8a43b2cc04873cd6236126d8f17f18f576ce0250d2ae65edccec178729d89945bbf45776f6747b6ef09e3ecf06a4bf5701b847ca9bc3831ad3a62d329ed524a2517035e115e2dbe163102fc215aac49a92b13e2be282964681cef742e36d792d4793500390f38661a4fadfbdabc722fa4ce4db640dc628642706a85b47a7ade8381ddd664a661b23ecf49a89777adea493b6f4a9e68d18a407e4c6154590100287ceae05c8c8add2f575d26d457cee92d76dc643d1e2bb5f485d6e9b4d0180a6691f0ae9e7f4e8d8c6776d2d305acf173e5edaf17287d53148468fce7a2cc3cc7e5675b9d9ab043fcf8adef3698922e8736d781c40992ff836f5ed85cf23a8fc6da79b913845bfe3cabcc72bc1a672bca98b6c70f0aa53414efaf9b544c95a029d965d06c84b3364a4d53aed9462bc1b1da1590a61dbd25b2b437256b3ee6aeb3d662cd36c580e044435f0f4a0109c190091cacf5570d0eb54c5c056c9786f00eda835649dc80decdcbb391fa04ca370ecf86b103574e035b1f6efcab4f9f5aa89fc7711852b0daaf4ca35a72ba2d898929d9bd958b48daaf0e4af6a0eda693a66249446b6d656d6265722d69642d3265454344534158210230627dc5e3d64a959581e44ebc60b593b87517bb501ae8bcfdc7b4b21a2dda2f67456c47616d616c582102179e1b352d573edeca6919748f4b905a015febe7bac04b82885d7e210fceb3ef614e590100dce1b2620945fac45054e1d9f6de47427e85ecf0d6fbbbfc35d3784f5c281d96c434f99a2f784e7a76373680741869c183a7e09931445d78d2f1b602008cbcbd81864066e292dba5627b779d2547bb6451cde23b3748def6dbbc4611c588af1a9963f345140070270c979703646ba3a0b04a49c5a230767e3867431ecc7819f01eec83a6b64c5cd94528c9de143b98b0b3b51ced0702227a9405dfc38d82dde103e565737a17c915a84603c581d3007a1fa31f8e217e562f018a2e870622b3ed16f5205e9af7ea53be9fd29b95f4c3cbe340a2c119be0c5998fb929adfe59a64df1881562647a28bd8ddf2dcef7182895bf7eb2552b20e95e89d18fd02df7fc1615359010004e3ae6b395ce0bb2be593b90d508fbaf7f2953406423631b9dfdc042f043e6c48112dd6073d9d5b381b8e6c719eea5a1db7c85797dbcd1cb140e423ff3ad7dc792dd9177d648c67a3b5c31e3102fba1bf7211f21a9c9e7a719e87d35e1caea0c5bcf13214876c55188d06444c18aa7f8aad0b856ec3e1ec8c80f9db2b705e46874a6ae354c0b3689cf8e9b6ca2f381c9792a00dddce6e5549dae0da5bd85cabcfd4e436216dc1f4d57e45e3d204b2ee72d7f7d27c230045f2bbb418fa32dce4799dbbb6d4929a34cca24e9642ad8df4f8ec09f089f9a76bb024398704c4614c095b51210f8da55467d62f036248ed2c57a4d13ba43e44c3e8d9c65b3010aeaf61545901003f99e748e69cc8238c2150beb9b4d25d3b495db676d75d6e11abe138f9b7a52daf703c21e8546ca35bcf8ef87df77ee4bc096f34472f5deab69bf6be9d1d179e61bbab9b2fb6c041e6c733bd0b8d4a0e6fd94070452c946ec2b695ea9cc5e7fe519b049b250ef692a6be23ef508ed3c98957810c96fa2d4345b9f27debeb5b0e4b705e25f3cc484efb0487831f4b2efb2f4fa8c6473e960f34d57a2f9a0bfa791dc59e2def63712e17b50f5082f58d08b79581b61fa22b5dbf6b0b8c1e5142527f2576b0c81dff96ba02757ffb33b52efc3bfbb1775d2f282ba8cdcb91b20f6f4fc3d4f823ae4349acf4d2d7eafeac49c78beede2d8c70b6b208e9b4c2a859d4a66249446b6d656d6265722d69642d3365454344534158210371f3ab8aa11ebaf368eae875398791ecfac356fe35adb4ef9c727645ed311a9067456c47616d616c582102bb4a36f56b23af462bca403704e0ef456f3409155c5c059efc1d67cea119317b614e590100b0dc1900d8ee92e5ac175eed6b3afa742eaf3ca70d4a77848a55644daed3bdd04525d883aab2ec0aaa294d88e6b3edd421ffa8b627985a480f950b0ac4137f0c69e8968fca187394e4c17bb1804a33d84b2e56862592948aa569b0eb755047a248ba2b3b7284213ea9f667317a5098bab688d1ac7825324a9655c95f829b465781bc499a18f31f5d993acbd9b4efdb91e70084e9afe30e5a142a23ba6e0c069259b49a2a5dc5c6e593d27caf55a33d1dd34219c3da16e79958c2d11f0f5e753cde9ae67c4185e7fa6a695d2251033c38648bf9701d448ba9d4c939ce0c5194d71de5d092196f8f261b58cbe6648152b3262082f9b3527dbec8d4fa40048ae22961535901002a74d1ecf0a86489b4a1382998b58969ef6b5c81985d0587af746ca9893dac867725dbbc34dc7c4455a23a0f5d207294124c1e43ed0e1170f9ce3ea0bba9c18bf7a723cf8faa2c42134f21bf1215ac11e44cfd4233c401b54d2d968fa8cc8d9c80d52a38c328b08387829c9af71144371b9b95e3f0e192ad31cb55fe6a8e6d4f293476264e3fb866171b4a663d5f5bf1fafe2936547f39a46e5d44e6560ffc6bbfdbe82cf29e2389192cfece8d09492a6a036051676e69a7cc9c1a2f1ddf6c92b97516d9e5ab9293e751d120784664f20881715f12934e1116b341364b9191911b53c00ef98fc0bd667deae325446e28d902020e33451ccb76be94eb96bc03f96154590100019e725d7ffe4ce282a4c47e6559e86ca8aaa527a5db6a5f140a3948a1cdba6d891d06b239fe9c99f6d7c1db74ddeb05f8e0bec146aef7cce7133114e38c9f88810c30caa50cc6d3bc6dd01714e0a0d9bbf62bfd5ac301847ffae077ba2874b2586369c2e2345f26da526f3649b0749e60bf0d5f768009ee4d782ab1b489b25da1ce6dc434d201052bab5674c12852b082359f397278a8b339e924fb217348b383f6246247dcbce2b71b350f3cc94fb8c15cbfcbf5faa1951d2fa53f5a531cd4c877b9754d1d4ef0e817685c74e1dc093303fa4a89e910fb0bd8db1cd0b9e95660152a26fef680cdd5082d289da511ba4e278d4c69e8638cda4abe15454c0b77",
	"member-id-3": "02bf0a7fa4b7905a0de5ab60a5322529e1a591ddd1ee53df82e751e8adb4bed08c;a96249446b6d656d6265722d69642d33695468726573686f6c6402654543445341582034d25e0913c3176d8363bbfd85345088bb295475cd445ea6957b878e948c393b67456c47616d616c5820b0f245b285983d7868bb1e075c8a1568e7e36b64c5a6677db73f262fa6fccced61505880cd174d4298454f00298d03d7179326b7043e643077c5404d1386e361485051bc2f52d1b1b681628709a2e70632f027744d8fccb61b3390229a7b6a5b4430834f2e3dc2ea3eff00adc17e3ba3163c53b5acf1307350f490f7432c2b56dfb7861622e5008e7e3eae88a3e29209b62655c2d2a2e063af80b40a88fffd245a13a62761515880dcc2d06407278fa797732c8a46c3c29d78002dcb6d85de1930d47954fd1a18bcad67ac4b562cb20d8445829a5a4bfa1fda16933eaea536298151e6773278e319d89ea717214981014c042ed9b8722667f477f4d086d148960e072d495bb1856b9b68033ac49dc0af525cbd7bb4b398391be71cb8cf58e545714bfda216fb372f635249445820b13663e6e4c5053c40bddee742ce959852f89acf55167a3ff0cc66bfa2f5a9f968436861696e4b65795820f555b08a9871213c0d52fee12e1bd365990b956880491b2b1a106f84584aa3a2665075626c696384a66249446b6d656d6265722d69642d30654543445341582102d16b97011927008338404f56d64f2d984fa70699c91f2c8185602721a284e0d767456c47616d616c582102c1f8227a5994d7bf2d996a10489c816845cfe94a424e0e3da4b49e659cbb20b8614e590100e3673176bcd18a5dcb6bb7a8027c71d26efb89205e521a9ee498ac24bbefb3d25b9e12429599fbedc864d2e164c2dfcc00f04014dcd883a2327b095c4f76508c8c06c2841fd273952509c32e7cf727476457d6799d52219fdb3d24e977b5bb1b60f0fc3537c6357777f194b25b8a35afdb04929637a20d9615b32212831c5134456178760251dc3298fbed5f73b0d09489667ce16e87355011bdb6310fef6f2f7020cf5466e7e672fdefffa951cd4207df12d0e8f5bbb5823fc315ae3227433e16086ef89651042b3cdf4028751cfc7c78bd460424cd1787bf69f095c65818a3b08214eeb1c7c37be11eac68d88aa0c0f33d14d7a0f5b5a8192266dbba12ff9d6153590100c4b5aed987e4e31098f21f3184572054acaff47f6003f27cc89529cb2b5de4903a35c54ec9db439391961f256fb64b7fe6fff2bfdeca8a5406f4e78a3bf4e293d0adc698b130d159d35bc48f63fead1170d8f26cd73aa2380ec9c1515ba9febe9153e08bec01f6efd6ac02268c68d58868fbb9b1c1f71788befc995bd0e82d35ab9f0ad6a8cf4ba6319cb6c39a109f99513b5b04fbae532da64583be66ed88bffa1572151e28af9defecea3bd0f2b862dd8820fcc9c5bef12f71309fa23cb10a4a8d5db604c8577e356c83388043bdb0a58279d9de53c214e1dea1e2ad5a47bead3bbd33e71bc763a086140819f308980fc457547afa4c62d4a2601ec24cc2b9615459010029a560c8f3aaea766c9f31e61fd665c65b6ca4e72d1374484a8000d6f57711df03a95fbe1c7ebdc9359b902f08bc48ce06a116be5dc38416298cae1481b19689244016038f7e74bbed78b80afc806fab2b86d7af4d0f57d99ff9f075b0ab654c1314d4444eb006e99ab543b51a701f5972693003703b8dd8e5ad37f8f33e9f09f4f029ea7d3310dc2c00b89c3bd32ce2427306131cacafcf8e399f49756c38012d1f614090d38a1412752c95b08414dc4f8d940526b593720fdbe9a1053e3c7a89c380118fcec394e5c8f38ee6db241d5181eee58f82950a9b4e37a954440994b8a81e839ce9610627f49fdd1a6ec98dd5a4641b7c58b0d06e434d54b7f47339a66249446b6d656d6265722d69642d3165454344534158210205911bbe040e9b1e4cb1276d34f339cf56e2688d7d0e6c1d67df37ea9adf303f67456c47616d616c582102debdda71b628a8e0b8919064c2ba40d1297271281c7b87a2b911fc0ec42787ed614e590100bd4cdd83f18803265b8158f87cbd448c3f329946f04a074cca9cfc5bc898d1b34c44bc69ba124b5921739df3a773aafc64871cfa11e0d466daffe638c9595f5f84980f4b35b571d32387edd2cb59b51cd11bb475ab42482248d788d4176911bc718c37a51eda81d2d73d8e1d0d4f185bea33804ba114a546f76d3e24dede18c2419251bc48e0446ba7714d3327bd12c3e62ae60b130813604ecc240992d8af586c8f9b32c1324779c18b9e1080acb2ccf70e83a09ed57c141762702ce2383161f9bdb6f0ffdbcf3c37df219f301db4da185d6e52e9060b6173dc01a78fc7bc0e0153383e515390d7992a6c828d37c0cbaa306e812a55714b9b212cbd19a8e5fd615359010016c7797854a64e2cd8b75550e6930fa74dd05d1524ab914e96ebe12c755f64afbd39387d4c358fe2a182d7f3fdf4b5a61416cfccf203e5369a358467d94580ba6b4cc98b5bf180101e8787d5e39984fc6235ad2526efb38ef3606654b80ea93c1c35937123c8a43b2cc04873cd6236126d8f17f18f576ce0250d2ae65edccec178729d89945bbf45776f6747b6ef09e3ecf06a4bf5701b847ca9bc3831ad3a62d329ed524a2517035e115e2dbe163102fc215aac49a92b13e2be282964681cef742e36d792d4793500390f38661a4fadfbdabc722fa4ce4db640dc628642706a85b47a7ade8381ddd664a661b23ecf49a89777adea493b6f4a9e68d18a407e4c6154590100287ceae05c8c8add2f575d26d457cee92d76dc643d1e2bb5f485d6e9b4d0180a6691f0ae9e7f4e8d8c6776d2d305acf173e5edaf17287d53148468fce7a2cc3cc7e5675b9d9ab043fcf8adef3698922e8736d781c40992ff836f5ed85cf23a8fc6da79b913845bfe3cabcc72bc1a672bca98b6c70f0aa53414efaf9b544c95a029d965d06c84b3364a4d53aed9462bc1b1da1590a61dbd25b2b437256b3ee6aeb3d662cd36c580e044435f0f4a0109c190091cacf5570d0eb54c5c056c9786f00eda835649dc80decdcbb391fa04ca370ecf86b103574e035b1f6efcab4f9f5aa89fc7711852b0daaf4ca35a72ba2d898929d9bd958b48daaf0e4af6a0eda693a66249446b6d656d6265722d69642d3265454344534158210230627dc5e3d64a959581e44ebc60b593b87517bb501ae8bcfdc7b4b21a2dda2f67456c47616d616c582102179e1b352d573edeca6919748f4b905a015febe7bac04b82885d7e210fceb3ef614e590100dce1b2620945fac45054e1d9f6de47427e85ecf0d6fbbbfc35d3784f5c281d96c434f99a2f784e7a76373680741869c183a7e09931445d78d2f1b602008cbcbd81864066e292dba5627b779d2547bb6451cde23b3748def6dbbc4611c588af1a9963f345140070270c979703646ba3a0b04a49c5a230767e3867431ecc7819f01eec83a6b64c5cd94528c9de143b98b0b3b51ced0702227a9405dfc38d82dde103e565737a17c915a84603c581d3007a1fa31f8e217e562f018a2e870622b3ed16f5205e9af7ea53be9fd29b95f4c3cbe340a2c119be0c5998fb929adfe59a64df1881562647a28bd8ddf2dcef7182895bf7eb2552b20e95e89d18fd02df7fc1615359010004e3ae6b395ce0bb2be593b90d508fbaf7f2953406423631b9dfdc042f043e6c48112dd6073d9d5b381b8e6c719eea5a1db7c85797dbcd1cb140e423ff3ad7dc792dd9177d648c67a3b5c31e3102fba1bf7211f21a9c9e7a719e87d35e1caea0c5bcf13214876c55188d06444c18aa7f8aad0b856ec3e1ec8c80f9db2b705e46874a6ae354c0b3689cf8e9b6ca2f381c9792a00dddce6e5549dae0da5bd85cabcfd4e436216dc1f4d57e45e3d204b2ee72d7f7d27c230045f2bbb418fa32dce4799dbbb6d4929a34cca24e9642ad8df4f8ec09f089f9a76bb024398704c4614c095b51210f8da55467d62f036248ed2c57a4d13ba43e44c3e8d9c65b3010aeaf61545901003f99e748e69cc8238c2150beb9b4d25d3b495db676d75d6e11abe138f9b7a52daf703c21e8546ca35bcf8ef87df77ee4bc096f34472f5deab69bf6be9d1d179e61bbab9b2fb6c041e6c733bd0b8d4a0e6fd94070452c946ec2b695ea9cc5e7fe519b049b250ef692a6be23ef508ed3c98957810c96fa2d4345b9f27debeb5b0e4b705e25f3cc484efb0487831f4b2efb2f4fa8c6473e960f34d57a2f9a0bfa791dc59e2def63712e17b50f5082f58d08b79581b61fa22b5dbf6b0b8c1e5142527f2576b0c81dff96ba02757ffb33b52efc3bfbb1775d2f282ba8cdcb91b20f6f4fc3d4f823ae4349acf4d2d7eafeac49c78beede2d8c70b6b208e9b4c2a859d4a66249446b6d656d6265722d69642d3365454344534158210371f3ab8aa11ebaf368eae875398791ecfac356fe35adb4ef9c727645ed311a9067456c47616d616c582102bb4a36f56b23af462bca403704e0ef456f3409155c5c059efc1d67cea119317b614e590100b0dc1900d8ee92e5ac175eed6b3afa742eaf3ca70d4a77848a55644daed3bdd04525d883aab2ec0aaa294d88e6b3edd421ffa8b627985a480f950b0ac4137f0c69e8968fca187394e4c17bb1804a33d84b2e56862592948aa569b0eb755047a248ba2b3b7284213ea9f667317a5098bab688d1ac7825324a9655c95f829b465781bc499a18f31f5d993acbd9b4efdb91e70084e9afe30e5a142a23ba6e0c069259b49a2a5dc5c6e593d27caf55a33d1dd34219c3da16e79958c2d11f0f5e753cde9ae67c4185e7fa6a695d2251033c38648bf9701d448ba9d4c939ce0c5194d71de5d092196f8f261b58cbe6648152b3262082f9b3527dbec8d4fa40048ae22961535901002a74d1ecf0a86489b4a1382998b58969ef6b5c81985d0587af746ca9893dac867725dbbc34dc7c4455a23a0f5d207294124c1e43ed0e1170f9ce3ea0bba9c18bf7a723cf8faa2c42134f21bf1215ac11e44cfd4233c401b54d2d968fa8cc8d9c80d52a38c328b08387829c9af71144371b9b95e3f0e192ad31cb55fe6a8e6d4f293476264e3fb866171b4a663d5f5bf1fafe2936547f39a46e5d44e6560ffc6bbfdbe82cf29e2389192cfece8d09492a6a036051676e69a7cc9c1a2f1ddf6c92b97516d9e5ab9293e751d120784664f20881715f12934e1116b341364b9191911b53c00ef98fc0bd667deae325446e28d902020e33451ccb76be94eb96bc03f96154590100019e725d7ffe4ce282a4c47e6559e86ca8aaa527a5db6a5f140a3948a1cdba6d891d06b239fe9c99f6d7c1db74ddeb05f8e0bec146aef7cce7133114e38c9f88810c30caa50cc6d3bc6dd01714e0a0d9bbf62bfd5ac301847ffae077ba2874b2586369c2e2345f26da526f3649b0749e60bf0d5f768009ee4d782ab1b489b25da1ce6dc434d201052bab5674c12852b082359f397278a8b339e924fb217348b383f6246247dcbce2b71b350f3cc94fb8c15cbfcbf5faa1951d2fa53f5a531cd4c877b9754d1d4ef0e817685c74e1dc093303fa4a89e910fb0bd8db1cd0b9e95660152a26fef680cdd5082d289da511ba4e278d4c69e8638cda4abe15454c0b77",
}
