package ipfs

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"io"
	"sync"
	"time"

	iface "github.com/ipfs/boxo/coreiface"
	"github.com/ipfs/boxo/coreiface/options"
	ma "github.com/multiformats/go-multiaddr"

	ipfspath "github.com/ipfs/boxo/coreiface/path"
	ipns "github.com/ipfs/boxo/ipns"
	path "github.com/ipfs/boxo/path"
	"github.com/multiformats/go-multibase"

	logging "github.com/ipfs/go-log/v2"

	blocks "github.com/ipfs/go-block-format"
	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	cfg "github.com/ipfs/kubo/config"
	ipfsCore "github.com/ipfs/kubo/core"
	coreapi "github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/node/libp2p"
	repo "github.com/ipfs/kubo/repo"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/allisterb/flubber/w3s"
	"github.com/ipfs/go-cid"
	ipldlegacy "github.com/ipfs/go-ipld-legacy"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/linking"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	mh "github.com/multiformats/go-multihash"
)

type IPFSCore struct {
	Ctx      context.Context
	Api      iface.CoreAPI
	Node     ipfsCore.IpfsNode
	Shutdown func()
	LS       linking.LinkSystem
	W3S      w3s.Client
}

type IPFSLinkWriter struct {
	ctx  context.Context
	ipfs iface.CoreAPI
	cid  cid.Cid
	w3s  w3s.Client
	log  *logging.ZapEventLogger
	data bytes.Buffer
}

type SubscriptionMessage struct {
	Did     string
	Content string
	Time    time.Time
	Topic   string
	Read    bool
}

var log = logging.Logger("flubber/ipfs")
var Messages = list.New()
var Subscriptions map[string]iface.PubSubSubscription = make(map[string]iface.PubSubSubscription)
var bootstrapAddresses = []string{
	"/ip4/38.132.215.232/tcp/4001/p2p/QmWHXv9o2wBTiuwV1e2bAqfJVK3poLo13DhpCXyWjowDn2",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.248.121.151/tcp/4001/p2p/QmPNLmhuSmGSYghhXxfpwDfvmkEnX1YNAxMCeoS332pwCz",
}

func (w *IPFSLinkWriter) Write(d []byte) (int, error) {
	return w.data.Read(d)
}

func (store *IPFSCore) Has(ctx context.Context, key string) (bool, error) {
	_, cid, err := cid.CidFromBytes([]byte(key))
	if err != nil {
		log.Errorf("could not create CID from key string %s: %v", key, err)
		return false, err
	}
	_, err = store.Api.Block().Stat(ctx, ipfspath.IpldPath(cid))
	return err != nil, err
}

func (store *IPFSCore) Get(ctx context.Context, key string) ([]byte, error) {
	_, k, err := cid.CidFromBytes([]byte(key))
	if err != nil {
		log.Errorf("could not create CID from key string %s: %v", key, err)
		return []byte{}, err
	}
	log.Infof("getting IPLD node %v from IPFS DAG: %v", k)
	r, err := store.Api.Block().Get(ctx, ipfspath.IpldPath(k))
	if err != nil {
		log.Errorf("could not get IPLD node %v from IPFS DAG: %v", key, err)
		return []byte{}, err
	}
	buf, _ := io.ReadAll(r)
	b, _ := blocks.NewBlockWithCid(buf, k)
	ul, err := ipldlegacy.DecodeNode(ctx, b)
	log.Infof("got IPLD node %v from IPFS DAG", k, err)
	return ul.RawData(), err
}

func (store *IPFSCore) Put(ctx context.Context, key string, data []byte) error {
	_, k, err := cid.CidFromBytes([]byte(key))
	if err != nil {
		log.Errorf("could not create CID from key string %s: %v", key, err)
		return err
	}
	log.Infof("putting IPLD block %v to IPFS DAG...", k)
	b, _ := blocks.NewBlockWithCid(data, k)
	ul, err := ipldlegacy.DecodeNode(ctx, b)
	if err != nil {
		log.Errorf("could not decode ILPD node data as LegacyNode: %v", err)
		return err
	}
	err = store.Api.Dag().Pinning().Add(ctx, ul)
	if err == nil {
		log.Infof("put IPLD block %v to IPFS DAG", k)
	} else {
		log.Errorf("error putting IPLD block %v to local IPFS DAG: %v", k, err)
		return err
	}
	_, err = PinIPLDBlockToW3S(ctx, store.Api, store.W3S.GetAuthToken(), b)
	if err == nil {
		log.Infof("put IPLD block %v to IPFS DAG", k)
	} else {
		log.Errorf("error putting IPLD block %v to IPFS DAG: %v", k, err)
	}
	return err
}

func (store *IPFSCore) OpenRead(lnkCtx linking.LinkContext, lnk datamodel.Link) (io.Reader, error) {
	_, k, err := cid.CidFromBytes([]byte(lnk.Binary()))
	if err != nil {
		log.Errorf("could not create CID from key string %s: %v", lnk.Binary(), err)
		return nil, err
	}
	log.Infof("getting IPLD link %v from IPFS DAG: %v", k)
	r, err := store.Api.Block().Get(lnkCtx.Ctx, ipfspath.IpldPath(k))
	if err != nil {
		log.Errorf("could not get IPLD node %v from IPFS DAG: %v", k, err)
		return nil, err
	}
	buf, _ := io.ReadAll(r)
	b, _ := blocks.NewBlockWithCid(buf, k)
	ul, err := ipldlegacy.DecodeNode(lnkCtx.Ctx, b)
	log.Infof("got IPLD link %v from IPFS DAG", k, err)
	return bytes.NewReader(ul.RawData()), err
}

func (store *IPFSCore) OpenWrite(lnkCtx linking.LinkContext, lnk datamodel.Link) (io.Writer, linking.BlockWriteCommitter, error) {
	_, k, err := cid.CidFromBytes([]byte(lnk.Binary()))
	if err != nil {
		log.Errorf("could not create CID from key string %s: %v", lnk.Binary(), err)
		return nil, nil, err
	}
	lw := IPFSLinkWriter{
		ctx:  lnkCtx.Ctx,
		ipfs: store.Api,
		cid:  k,
		w3s:  store.W3S,
		log:  log,
		data: bytes.Buffer{},
	}
	return &lw, lw.BlockWriteCommit, nil
}

func (w *IPFSLinkWriter) BlockWriteCommit(lnk datamodel.Link) error {
	/*
		b, err := blocks.NewBlockWithCid(w.data.Bytes(), w.cid)
		if err != nil {
			return err
		}
		w.log.Infof("putting IPLD block %v to IPFS...", w.cid)
		s, err := w.ipfs.Block().Put(w.ctx, bytes.NewReader(b.RawData()))
		if err != nil {
			w.log.Infof("put IPLD block %v to IPFS at path %s with size %v bytes", w.cid, s.Path(), s.Size())
		} else {
			w.log.Errorf("error putting IPLD block %v to IPFS: %v", w.cid, err)
			return err
		}
		w.log.Infof("putting IPLD block %v to Web3.Storage...", w.cid)
		var cardata bytes.Buffer
		err = w3s.WriteCar(w.ctx, w.ipfs.Dag(), []cid.Cid{b.Cid()}, &cardata)
		if err != nil {
			w.log.Errorf("could not serialize block %v as CAR: %v", b.Cid(), err)
			return err
		}
		pcid, err := w.w3s.PutCar(w.ctx, &cardata)
		if err != nil {
			w.log.Infof("put IPLD block %v to Web3.Storage at %v", w.cid, pcid)
		} else {
			w.log.Errorf("error putting IPLD block %v to Web3.Storage: %v", w.cid, err)
		}
		return err
	*/
	return nil
}

func GenerateIPNSKeyPair() ([]byte, []byte, error) {
	priv, pub, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		log.Errorf("error generating RSA 2048-bit keypair: %v", err)
		return []byte{}, []byte{}, err
	}
	privkeyb, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		log.Errorf("error marshalling RSA 2048-bit private key: %v", err)
		return []byte{}, []byte{}, err
	}
	pubkeyb, err := crypto.MarshalPublicKey(pub)
	if err != nil {
		log.Errorf("error marshalling RSA 2048-bit public key: %v", err)
		return []byte{}, []byte{}, err
	}
	return privkeyb, pubkeyb, err
}

func GenerateIPFSNodeKeyPair() ([]byte, []byte, error) {
	priv, pub, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		log.Errorf("error generating RSA 2048-bit keypair: %v", err)
		return []byte{}, []byte{}, err
	}
	privkeyb, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		log.Errorf("error marshalling RSA 2048-bit private key: %v", err)
		return []byte{}, []byte{}, err
	}
	pubkeyb, err := crypto.MarshalPublicKey(pub)
	if err != nil {
		log.Errorf("error marshalling RSA 2048-bit public key: %v", err)
		return []byte{}, []byte{}, err
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		panic(err)
	}
	log.Infof("generated identity %s", id.Pretty())
	return privkeyb, pubkeyb, err
}

func GetIPFSNodeIdentity(pubb []byte) peer.ID {
	pub, err := crypto.UnmarshalPublicKey(pubb)
	if err != nil {
		panic(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return id
}

func GetIPFSNodeIdentityFromPublicKeyName(key string) (peer.ID, error) {
	_, b, err := multibase.Decode(key)
	if err != nil {
		return "", fmt.Errorf("could not decode %s as a multibase string: %v", key, err)
	}
	_, c, err := cid.CidFromBytes(b)
	if err != nil {
		return "", fmt.Errorf("could not decode %s as a CID: %v", key, err)
	}
	id, err := peer.FromCid(c)
	if err != nil {
		return "", fmt.Errorf("could not get peer ID from CID %v : %v", c, err)
	}
	return id, nil
}

func GetIPNSPublicKeyName(pubb []byte) (string, error) {
	pub, err := crypto.UnmarshalPublicKey(pubb)
	if err != nil {
		log.Errorf("could not unmarshal IPNS public key: %v", err)
		return "", err
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		log.Errorf("could not get peer ID public key: %v", err)
		return "", err
	}
	return peer.ToCid(pid).StringOfBase(multibase.Base36)
}

func initIPFSRepo(ctx context.Context, privkey []byte, pubkey []byte) repo.Repo {
	pid := GetIPFSNodeIdentity(pubkey)
	c := cfg.Config{}
	c.Pubsub.Enabled = cfg.True
	//c.Experimental.AcceleratedDHTClient = f
	//c.Pubsub.Router = "floodsub"
	c.Ipns.UsePubsub = cfg.True
	c.Bootstrap = bootstrapAddresses
	c.Addresses.Swarm = []string{"/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic", "/ip4/127.0.0.1/tcp/4001", "/ip4/127.0.0.1/udp/4001/quic"}
	c.Identity.PeerID = pid.Pretty()
	c.Identity.PrivKey = base64.StdEncoding.EncodeToString(privkey)

	return &repo.Mock{
		D: dsync.MutexWrap(ds.NewMapDatastore()),
		C: c,
	}
}

func StartIPFSNode(ctx context.Context, privkey []byte, pubkey []byte) (*IPFSCore, error) {
	log.Infof("starting IPFS node %s...", GetIPFSNodeIdentity(pubkey).Pretty())
	node, err := ipfsCore.NewNode(ctx, &ipfsCore.BuildCfg{
		Online:  true,
		Routing: libp2p.DHTOption,
		Repo:    initIPFSRepo(ctx, privkey, pubkey),
		ExtraOpts: map[string]bool{
			"pubsub": true,
		},
		Permanent: true,
	})
	if err != nil {
		log.Errorf("error staring IPFS node %s: %v", GetIPFSNodeIdentity(pubkey).Pretty(), err)
		return nil, err
	}
	pubk, _ := GetIPNSPublicKeyName(pubkey)
	log.Infof("IPFS node %s (%v) started", node.Identity.Pretty(), pubk)
	c, e := coreapi.NewCoreAPI(node)
	if e != nil {
		return nil, e
	} else {
		shutdown := func() {
			log.Infof("shutting down IPFS node %s...", node.Identity.Pretty())
			node.Close()
			log.Infof("IPFS node %s shutdown completed", node.Identity.Pretty())
		}
		core := IPFSCore{
			Ctx:      ctx,
			Api:      c,
			Node:     *node,
			Shutdown: shutdown,
		}
		c, err := w3s.NewClient(w3s.WithToken("none"))
		if err != nil {
			log.Errorf("could not create W3S client: %v", err)
			return nil, err
		}
		core.W3S = c

		lsys := cidlink.DefaultLinkSystem()
		lsys.SetReadStorage(&core)
		lsys.SetWriteStorage(&core)
		core.LS = lsys

		_, err = core.Api.PubSub().Subscribe(ctx, "flubber")
		if err != nil {
			log.Errorf("could not subscribe to flubber topic: %v", err)
			return nil, err
		}
		err = core.Api.PubSub().Publish(ctx, "flubber", []byte{byte(1)})
		connectToPeers(ctx, core.Api, bootstrapAddresses)
		return &core, err
	}
}

func PublishIPNSRecordForDAGNode(ctx context.Context, ipfscore IPFSCore, authtoken string, cid cid.Cid, keyname string, privkey []byte, pubkey []byte) error {
	p := ipfspath.IpldPath(cid)
	//_, err := ipfscore.Node.Repo.Keystore().
	//if err != nil {
	//	return fmt.Errorf("could not get key %s from IPFS node keystore: %v", keyname, err)
	//}
	r, err := ipfscore.Api.Name().Publish(ctx, p)
	if err != nil {
		return fmt.Errorf("error publishing IPNS record for %v using IPFS node key %s: %v", p, keyname, err)
	} else {
		log.Infof("created IPNS record on DHT for path %v", r.Value())
		return err
	}
}

func PinIPFSBlockToW3S(ctx context.Context, ipfs iface.CoreAPI, authToken string, block *blocks.BasicBlock) error {
	c, err := w3s.NewClient(w3s.WithToken(authToken))
	if err != nil {
		log.Errorf("could not create W3S client: %v", err)
		return err
	}
	l, err := ipfs.Swarm().LocalAddrs(ctx)
	if err != nil {
		log.Errorf("could not get IPFS node local addresses: %v", err)
		return err
	}
	us := make([]w3s.PinOption, len(l))
	for i := range l {
		us[i] = w3s.WithPinOrigin(l[i].String())
	}
	r, err := c.Pin(ctx, block.Cid(), us[0])
	if err != nil {
		return err
	} else {
		log.Infof("IPFS block %v pinned using Web3.Storage pinning service at %v", block.Cid(), r.Pin.Cid)
		return err
	}
}

func PinIPLDBlockToW3S(ctx context.Context, ipfsNode iface.CoreAPI, authToken string, block *blocks.BasicBlock) (cid.Cid, error) {
	log.Infof("pinning IPLD block %v using Web3.Storage pinning service...", block.Cid())
	c, err := w3s.NewClient(w3s.WithToken(authToken))
	if err != nil {
		log.Errorf("could not create W3S client: %v", err)
		return cid.Cid{}, err
	}
	var buf bytes.Buffer
	err = w3s.WriteCar(ctx, ipfsNode.Dag(), []cid.Cid{block.Cid()}, &buf)
	if err != nil {
		log.Errorf("could not serialize block %v as CAR: %v", block.Cid(), err)
		return cid.Cid{}, err
	}
	pcid, err := c.PutCar(ctx, &buf)
	if err != nil {
		log.Errorf("could not put block %v as CAR to W3S: %v", block.Cid(), err)
		return cid.Cid{}, err
	} else {
		log.Infof("pinned IPLD block %v using Web3.Storage pinning service at https://ipfs.io/ipfs/%v", block.Cid(), pcid)
		return pcid, err
	}
}

func GetIPNSRecordFromW3S(ctx context.Context, authToken string, name string) (cid.Cid, error) {
	c, err := w3s.NewClient(w3s.WithToken(authToken))
	if err != nil {
		log.Errorf("could not create W3S client: %v", err)
		return cid.Cid{}, err
	}
	r, err := c.GetName(ctx, name)
	if err != nil {
		log.Errorf("could not lookup name %s on Web3.Storage: %v", name, err)
	}
	if r == nil {
		log.Infof("name %s does not exist on Web3.Storage", name)
		return cid.Undef, err
	}
	v := string(r.GetValue())
	p := path.FromString(v)
	log.Infof("IPNS name points to path %v", p)
	return cid.Parse(p.Segments()[1])
}

func PublishIPNSRecordForDAGNodeToW3S(ctx context.Context, authToken string, cid cid.Cid, privkey []byte, pubkey []byte) error {
	name, err := GetIPNSPublicKeyName(pubkey)
	if err != nil {
		return err
	}

	p := ipfspath.IpfsPath(cid).String()
	log.Infof("publishing DAG node %v at path %s to IPNS name %s using Web3.Storage...", cid, p, name)
	c, err := w3s.NewClient(w3s.WithToken(authToken))
	if err != nil {
		log.Errorf("could not create W3S client: %v", err)
		return err
	}
	sk, err := crypto.UnmarshalPrivateKey(privkey)
	if err != nil {
		log.Errorf("could not unmarshal IPNS private key: %v", err)
		return err
	}
	var seq uint64 = 1
	r, err := c.GetName(ctx, p)
	if r != nil && err == nil {
		seq = r.GetSequence() + 1
	}

	nr, err := ipns.Create(sk, []byte(p), seq, time.Now().Add(time.Hour*48), 0)
	if err != nil {
		log.Errorf("could not create new IPNS record for path %v: %v", p, err)
		return err
	}
	pk, err := crypto.UnmarshalPublicKey(pubkey)
	if err != nil {
		log.Errorf("could not unmarshal IPNS public key: %v", err)
		return err
	}
	if err = ipns.EmbedPublicKey(pk, nr); err != nil {
		log.Errorf("could not embed IPNS public key in record: %v", err)
		return err
	}

	err = c.PutName(ctx, nr, name)
	if err == nil {
		log.Infof("published DAG node %v at path %s to IPNS name %s using Web3.Storage", cid, p, name)
	} else {
		log.Errorf("could not publish DAG node %v at path %s to IPNS name %s using Web3.Storage: %v", cid, p, name, err)
	}
	return err
}

var IPLDLinkPrototype cidlink.LinkPrototype = cidlink.LinkPrototype{
	Prefix: cid.Prefix{
		Version:  1,           // Usually '1'.
		Codec:    cid.DagJSON, // 0x71 means "dag-cbor" -- See the multicodecs table: https://github.com/multiformats/multicodec/
		MhType:   mh.SHA3_384, // 0x20 means "sha2-512" -- See the multicodecs table: https://github.com/multiformats/multicodec/
		MhLength: 48,          // sha2-512 hash has a 64-byte sum.
	}}

var IPLDNodePrototype datamodel.NodePrototype = basicnode.Prototype.Any

func SubscribeToTopic(ctx context.Context, ipfscore IPFSCore, topic string) error {
	s, err := ipfscore.Api.PubSub().Subscribe(ctx, topic)
	if err != nil {
		return err
	}
	Subscriptions[topic] = s
	return err
}

func GetSubscriptionTopics(ctx context.Context, ipfscore IPFSCore) ([]string, error) {
	return ipfscore.Api.PubSub().Ls(ctx)
}

func GetSubscriptionMessages(ctx context.Context, ipfscore IPFSCore, topic string) (*list.List, error) {
	s, found := Subscriptions[topic]
	if !found {
		return nil, fmt.Errorf("the subscription %s does not exist", topic)
	}
	var messages = list.New()
	//encoder, _ := mb.EncoderByName("base64url")
	for {
		_m, err := s.Next(ctx)
		if err == io.EOF || err == context.DeadlineExceeded || err == context.Canceled {
			log.Infof("%v", err)
			break
		} else if err != nil {
			log.Errorf("error retrieving message for subscription %s: %v", topic, err)
			continue
		} else {
			buf := bytes.NewBuffer(_m.Data())
			var m SubscriptionMessage
			dec := gob.NewDecoder(buf)
			err = dec.Decode(&m)
			if err != nil {
				log.Errorf("error decoding message: %v", err)
				continue
			} else {
				log.Infof("%v", m)
				messages.PushBack(m)
			}
		}
	}
	return messages, nil
}

func PublishSubscriptionMessage(ctx context.Context, ipfscore IPFSCore, topic string, message SubscriptionMessage) error {
	_, found := Subscriptions[topic]
	if !found {
		return fmt.Errorf("the subscription %s does not exist", topic)
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(message)
	if err != nil {
		return err
	}
	return ipfscore.Api.PubSub().Publish(ctx, topic, buf.Bytes())
}

func GetPeers(ctx context.Context, ipfscore IPFSCore, topic string) ([]peer.ID, error) {
	if topic == "" {
		return ipfscore.Api.PubSub().Peers(ctx)
	} else {
		return ipfscore.Api.PubSub().Peers(ctx, options.PubSub.Topic(topic))
	}
}

func connectToPeers(ctx context.Context, ipfs iface.CoreAPI, peers []string) error {
	var wg sync.WaitGroup
	peerInfos := make(map[peer.ID]*peer.AddrInfo, len(peers))
	for _, addrStr := range peers {
		addr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			return err
		}
		pii, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			return err
		}
		pi, ok := peerInfos[pii.ID]
		if !ok {
			pi = &peer.AddrInfo{ID: pii.ID}
			peerInfos[pi.ID] = pi
		}
		pi.Addrs = append(pi.Addrs, pii.Addrs...)
	}

	wg.Add(len(peerInfos))
	for _, peerInfo := range peerInfos {
		go func(peerInfo *peer.AddrInfo) {
			defer wg.Done()
			err := ipfs.Swarm().Connect(ctx, *peerInfo)
			if err != nil {
				log.Infof("failed to connect to %s: %s", peerInfo.ID, err)
			}
		}(peerInfo)
	}
	wg.Wait()
	return nil
}
