# Mixin Safe

Mixin Safe is an advanced non-custodial solution that specializes in providing a multiplex cold wallet. The platform places strong emphasis on the significance of securing crypto keys, with its firm belief in the principle of "not your keys, not your coins". Mixin Safe leverages mature technologies such as multisig and MPC to offer its users the most secure and convenient solution for securing crypto keys.

Through the native Bitcoin multisig and timelock script, Mixin Safe offers a 2/3 multisig that comprises three keys, namely holder, signer, and observer. The BTC locked within the script can be spent only when both the holder and signer keys sign a transaction, provided that the timelock of one year is in effect. In case of key loss by the holder or signer, the observer can act as a rescuer after the timelock expired.

The signer key, which is MPC generated by Mixin Safe nodes, is controlled in a decentralized manner. Whenever a deposit is made into a safe account, Mixin Safe issues an equivalent amount of safeBTC to the account owner. To initiate a transaction with the holder key, the user needs to send safeBTC to the Mixin Safe network and sign the raw transaction with the holder key, thereby enabling the signer to sign the transaction together with the holder key.


## Prepare Holder Key

Currently, there aren't many Bitcoin wallets that can perform custom script signing, not even the bitcoin-core wallet. It's therefore recommended to use btcd, which can be accessed at https://github.com/btcsuite/btcd.

Using btcd, you can generate a public and private key pair using the following code:

```golang
priv, pub := btcec.PrivKeyFromBytes(seed)
fmt.Printf("public: %x\nprivate: %x\n", pub.SerializeCompressed(), priv.Serialize())

🔜
public: 039c2f5ebdd4eae6d69e7a98b737beeb78e0a8d42c7b957a0fbe0c41658d16ab40
private: 1b639e995830c253eb38780480440a72919f5448be345a574c545329f2df4d76
```

After generating the key pair, you will need to create a random UUID as the session ID. As example, the UUID `2e78d04a-e61a-442d-a014-dec19bd61cfe` will be used.


## Propose Safe Account

To ensure the efficiency of the network, every Mixin Safe account proposal costs 1USD. To propose an account, one simply needs to send 1USD to the network with a properly encoded memo. All messages sent to the safe network must be encoded as per the following operation structure:

```golang
type Operation struct {
	Id     string
	Type   uint8
	Curve  uint8
	Public string
	Extra  []byte
}

func (o *Operation) Encode() []byte {
	pub, err := hex.DecodeString(o.Public)
	if err != nil {
		panic(o.Public)
	}
	enc := common.NewEncoder()
	writeUUID(enc, o.Id)
	writeByte(enc, o.Type)
	writeByte(enc, o.Curve)
	writeBytes(enc, pub)
	writeBytes(enc, o.Extra)
	return enc.Bytes()
}
```

To send the account proposal, with the holder prepared from last step, the operation value should be like:

```golang
op := &Operation {
  Id: "2e78d04a-e61a-442d-a014-dec19bd61cfe",
  Type: 110,
  Curve: 1,
  Public: "039c2f5ebdd4eae6d69e7a98b737beeb78e0a8d42c7b957a0fbe0c41658d16ab40",
}
```

All above four fields above are mandatory for all safe network transactions, now we need to make the extra:

```golang
threshold := byte(1)
total := byte(1)
owners := []string{"fcb87491-4fa0-4c2f-b387-262b63cbc112"}
extra := []byte{threshold, total}
uid := uuid.FromStringOrNil(owners[0])
op.Extra = append(extra, uid.Bytes()...)
```

So the safe account proposal operation extra is encoded with threshold, owners count, and all owner UUIDs.

Then we can encode the operation and use it as a memo to send the account proposal transaction to safe network MTG:

```golang
memo := base64.RawURLEncoding.EncodeToString(op.Encode())
input := mixin.TransferInput{
  AssetID: "31d2ea9c-95eb-3355-b65b-ba096853bc18",
  Amount:  decimal.NewFromFloat(1),
  TraceID: op.Id,
  Memo:    memo,
}
input.OpponentMultisig.Receivers = []{
  "71b72e67-3636-473a-9ee4-db7ba3094057",
  "148e696f-f1db-4472-a907-ceea50c5cfde",
  "c9a9a719-4679-4057-bcf0-98945ed95a81",
  "b45dcee0-23d7-4ad1-b51e-c681a257c13e",
  "fcb87491-4fa0-4c2f-b387-262b63cbc112",
}
input.OpponentMultisig.Threshold = 4
```


## Approve Safe Account

After the account proposal transaction sent to the safe network MTG, you can monitor the Mixin Network transactions to decode your account details. But to make it easy, it's possible to just fetch it from the Safe HTTP API with the proposal UUID:

```
curl https://safe.mixin.dev/accounts/2e78d04a-e61a-442d-a014-dec19bd61cfe

🔜
{
  "accountant":"bc1qevu9qqpfqp4s9jq3xxulfh08rgyjy8rn76aj7e",
  "address":"bc1qzccxhrlm4p5l5rpgnns58862ckmsat7uxucqjfcfmg7ef6yltf3quhr94a",
  "id":"2e78d04a-e61a-442d-a014-dec19bd61cfe",
  "script":"6352670399f...96e9e0bc052ae",
  "state":"proposed"
}
```

You should have noticed that the request was made with the same session UUID we prepared at the first step. That address returned is our safe account address to receive BTC, but before using it, we must approve it with our holder key:


```golang
var buf bytes.Buffer
_ = wire.WriteVarString(&buf, 0, "Bitcoin Signed Message:\n")
_ = wire.WriteVarString(&buf, 0, fmt.Sprintf("APPROVE:%s:%s", sessionUUID, address))
hash := chainhash.DoubleHashB(buf.Bytes())
b, _ := hex.DecodeString(priv)
private, _ := btcec.PrivKeyFromBytes(b)
sig := ecdsa.Sign(private, hash)
fmt.Println(base64.RawURLEncoding.EncodeToString(sig.Serialize()))

🔜
MEUCIQCY3Gl1uocJR-qa2wVUuvK_gc-pOxzk8Zq_x_Hqv8iJbAIgXPbMuk-GiGsM3MJKmQ3haRzfDEKSBHArkgRF2NtxDOk
```

With the signature we send the request to safe network to prove that we own the holder key exactly:

```
curl https://safe.mixin.dev/accounts/2e78d04a-e61a-442d-a014-dec19bd61cfe -H 'Content-Type:application/json' \
  -d '{"address":"bc1qzccxhrlm4p5l5rpgnns58862ckmsat7uxucqjfcfmg7ef6yltf3quhr94a","signature":"MEUCIQCY3Gl1uocJR-qa2wVUuvK_gc-pOxzk8Zq_x_Hqv8iJbAIgXPbMuk-GiGsM3MJKmQ3haRzfDEKSBHArkgRF2NtxDOk"}'
```

Now we can deposit BTC to the address above, and you will receive safeBTC to the owner wallet.


## Propose Safe Transaction

After depositing some BTC to both the safe address and the accountant, we now want to send 0.000123 BTC to `bc1qevu9qqpfqp4s9jq3xxulfh08rgyjy8rn76aj7e`. To initiate the transaction, we require the latest Bitcoin chain head ID from the Safe network, which can be obtained by running the following command:

```
curl https://safe.mixin.dev/chains

🔜
[
  {
    "chain": 1,
    "head": {
      "fee": 13,
      "hash": "00000000000000000003aca37e964e47e89543e2b26495641c1fc4957500e46e",
      "height": 780626,
      "id": "155e4f85-d4b8-33f7-82e6-542711f1f26e"
    },
    "id": "c6d0c728-2624-429b-8e0d-d9d19b6592fa"
  }
]
```

Using the response we receive, we can determine that the Bitcoin transaction fee rate will be `13 Satoshis/vByte`. Before initiating the transaction, we need to estimate the fee and ensure that the accountant balance is sufficient to pay the total transaction fee. We will then include the head ID `155e4f85-d4b8-33f7-82e6-542711f1f26e` in the operation extra to indicate the fee rate we prefer.

Furthermore, we need to generate another random session ID, for which we will use `36c2075c-5af0-4593-b156-e72f58f9f421` as an example. With the holder prepared in the first step, the operation value should be as follows:

```golang
extra := uuid.FromStringOrNil("155e4f85-d4b8-33f7-82e6-542711f1f26e").Bytes()
extra = append(extra, []byte("bc1qevu9qqpfqp4s9jq3xxulfh08rgyjy8rn76aj7e")...)
op := &Operation {
  Id: "36c2075c-5af0-4593-b156-e72f58f9f421",
  Type: 112,
  Curve: 1,
  Public: "039c2f5ebdd4eae6d69e7a98b737beeb78e0a8d42c7b957a0fbe0c41658d16ab40",
  Extra: extra,
}
```

Next, we need to retrieve the safeBTC asset ID that was provided to us when we deposited BTC to the safe address. It is important to note that each safe account has its own unique safe asset ID, and for the safe account we created, the safeBTC asset ID is `94683442-3ae2-3118-bec7-069c934668c0`. We will use this asset ID to make a transaction to the Safe Network MTG as follows:

```golang
memo := base64.RawURLEncoding.EncodeToString(op.Encode())
input := mixin.TransferInput{
  AssetID: "94683442-3ae2-3118-bec7-069c934668c0",
  Amount:  decimal.NewFromFloat(0.000123),
  TraceID: op.Id,
  Memo:    memo,
}
input.OpponentMultisig.Receivers = []{
  "71b72e67-3636-473a-9ee4-db7ba3094057",
  "148e696f-f1db-4472-a907-ceea50c5cfde",
  "c9a9a719-4679-4057-bcf0-98945ed95a81",
  "b45dcee0-23d7-4ad1-b51e-c681a257c13e",
  "fcb87491-4fa0-4c2f-b387-262b63cbc112",
}
input.OpponentMultisig.Threshold = 4
```

Once the transaction is successfully sent to the Safe Network MTG, we can query the safe API to obtain the proposed raw transaction using the following command:

```
curl https://safe.mixin.dev/transactions/36c2075c-5af0-4593-b156-e72f58f9f421

🔜
{
  "fee":"0.00032181",
  "hash":"0e88c368c51fb24421b2a36d82674a5f058eb98d67da844d393b8df00ad2ad3f",
  "id":"36c2075c-5af0-4593-b156-e72f58f9f421",
  "raw":"00200e88c368c51fb...000000000000000007db5"
}
```


## Approve Safe Transaction

With the transaction proposed in previous step, we can decode the raw response to get the partially signed bitcoin transaction for the holder key to sign:

```golang
b, _ := hex.DecodeString(raw)
dec := common.NewDecoder(b)
hash, _ := dec.ReadBytes()
psbtBytes, _ := dec.ReadBytes()
fee, _ := dec.ReadUint64()
```

With the decoded PSBT, we can parse it to see all its inputs and outputs, and verify it's correct as we proposed. Then we sign all signature hashes with our holder private key:

```golang
script := theSafeAccountScript()
pkt, _ = psbt.NewFromRawBytes(bytes.NewReader(psbtBytes), false)
msgTx := pkt.UnsignedTx
for idx := range msgTx.TxIn {
	hash := sigHash(pkt, idx)
	sig := ecdsa.Sign(holder, hash).Serialize()
	pkt.Inputs[idx].PartialSigs = []*psbt.PartialSig{{
		PubKey:    holder.PubKey().SerializeCompressed(),
		Signature: sig,
	}}
}
raw := marshal(hash, pkt, fee)
fmt.Printf("raw: %x\n", raw)

var buf bytes.Buffer
_ = wire.WriteVarString(&buf, 0, "Bitcoin Signed Message:\n")
_ = wire.WriteVarString(&buf, 0, fmt.Sprintf("APPROVE:%s:%s", sessionUUID, msgTx.TxHash().String()))
hash := chainhash.DoubleHashB(buf.Bytes())
sig := ecdsa.Sign(holder, msg).Serialize()
fmt.Printf("signature: %s\n", base64.RawURLEncoding.EncodeToString(sig))
```

After we have the PSBT signed by holder private key, then we can send them to safe API:

```
curl https://safe.mixin.dev/transactions/36c2075c-5af0-4593-b156-e72f58f9f421 -H 'Content-Type:application/json' \
  -d '{"action":"approve","chain":1,"raw":"00200e88c368c51fb...000000000000000007db5","signature":"MEQCIDfROpqb2l5b9LD5RL865HsSDvKhSGI9a6RShQwdfI9jAiBWLep5ogVplOsBETaALGtlN6GmcHIASV_nU-AUhtN0mQ"}'
```

Once the transaction approval has succeeded, we will need to transfer 1USD to the observer, using the transaction hash as the memo to pay for it. After a few minutes, we should be able to query the transaction on a Bitcoin explorer and view its details.

https://blockstream.info/tx/0e88c368c51fb24421b2a36d82674a5f058eb98d67da844d393b8df00ad2ad3f?expand
