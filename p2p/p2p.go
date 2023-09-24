package p2p

import (
	"bufio"
	"container/list"
	"context"
	"encoding/json"
	"fmt"

	"github.com/ipfs/boxo/coreiface/options"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/allisterb/flubber/blockchain"
	"github.com/allisterb/flubber/did"
	"github.com/allisterb/flubber/ipfs"
)

type DM struct {
	Did     string
	Content string
}

var log = logging.Logger("flubber/p2p")

var Messages = list.New()

func SetDMStreamHandler(ipfscore ipfs.IPFSCore, apikey string) {
	ipfscore.Node.PeerHost.SetStreamHandler(protocol.ID("patrchat/0.1"), func(s network.Stream) {
		DMHandler(s, apikey)
	})
}

func DMHandler(_s network.Stream, apiKey string) {
	log.Infof("Incoming DM stream from %v...", _s.Conn().RemotePeer())
	_rw := bufio.NewReadWriter(bufio.NewReader(_s), bufio.NewWriter(_s))
	go func(s network.Stream, rw *bufio.ReadWriter) {
		dmb, err := rw.ReadBytes(byte(0))
		if err != nil {
			log.Errorf("error reading DM data from stream: %v", err)
			return
		}
		dm := DM{}
		json.Unmarshal(dmb, &dm)
		if !did.IsValid(dm.Did) {
			log.Errorf("the DID %s in the DM is not valid")
			return
		}
		did, _ := did.Parse(dm.Did)
		n, err := blockchain.ResolveENS(did.ID.ID, apiKey)
		if err != nil {
			log.Errorf("could not resolve ENS name %s: %v", did.ID.ID, err)
		}
		pid, err := ipfs.GetIPFSNodeIdentityFromPublicKeyName(n.IPFSPubKey)
		if err != nil {
			log.Errorf("could not get IPFS node identity from string %s: %v", n.IPFSPubKey, err)
			return
		}
		if s.Conn().RemotePeer() != pid {
			log.Errorf("the remote peer ID %v does not match the DID peer ID %v for %s", s.Conn().RemotePeer(), pid, did.ID.ID)
			return
		}
		log.Infof("the remote peer ID %v matches the DID peer ID %v for %s", s.Conn().RemotePeer(), pid, did.ID.ID)
		rw.WriteString("delivered")
		Messages.PushBack(dm)
		log.Infof("direct message from %v: %s", did.ID.ID, dm.Content)
	}(_s, _rw)
}

func SendDM(ctx context.Context, ipfscore ipfs.IPFSCore, apikey string, did string, text string) error {
	n, err := blockchain.ResolveENS(did, apikey)
	if err != nil {
		return fmt.Errorf("could not resolve ENS name %s: %v", did, err)
	}
	log.Infof("sending DM to DID %s...", did)
	pid, err := ipfs.GetIPFSNodeIdentityFromPublicKeyName(n.IPFSPubKey)
	if err != nil {
		return fmt.Errorf("could not get IPFS node identity from string %s: %v", n.IPFSPubKey, err)
	}
	log.Infof("IPFS node identity for %s is %v", did, pid)
	addr, err := ipfscore.Node.DHTClient.FindPeer(ctx, pid)
	if err != nil {
		peers, _ := ipfscore.Api.PubSub().Peers(ctx, options.PubSub.Topic("flubber"))
		var found bool = false
		for i := range peers {
			if peers[i] == pid {
				found = true
				log.Infof("found")
				break
			}
		}
		if !found {
			return fmt.Errorf("the node %v for DID %s is not online. Authenticated DMs cannot be sent to this DID right now", pid, did)
		}
	} else {
		log.Infof("the node %v for DID %s is online at address %v", pid, did, addr)
	}
	s, err := ipfscore.Node.PeerHost.NewStream(ctx, pid, protocol.ID("patrchat/0.1"))
	if err != nil {
		return fmt.Errorf("could not open new stream to peer %v: %v", pid, err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
	dm := DM{
		Did:     did,
		Content: text,
	}
	bdm, _ := json.Marshal(dm)
	_, err = rw.Write(append(bdm, byte(0)))
	if err != nil {
		return fmt.Errorf("could not write DM to stream to peer %v: %v", pid, err)
	}
	/*
		resp, err := rw.ReadBytes(byte(0))
		if err != nil {
			return fmt.Errorf("could not response to DM from stream to peer %v: %v", pid, err)
		}
		if string(resp) == "delivered" {
			log.Infof("delivered DM to DID %s", did)
			return nil
		} else {
			return fmt.Errorf("did not deliver DM to %s", did)
		}
	*/
	log.Infof("")
	return nil
}

func EventQueryHandler(s network.Stream) {

}
