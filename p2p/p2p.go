package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	Message string
	Time    time.Time
}

type Message struct {
	Did     string
	Content string
	Time    time.Time
	Topic   string
	Read    bool
}

var log = logging.Logger("flubber/p2p")
var DMs []DM

func SetDMStreamHandler(ipfscore ipfs.IPFSCore, apikey string) {
	ipfscore.Node.PeerHost.SetStreamHandler(protocol.ID("flubberchat/0.1"), func(s network.Stream) {
		log.Infof("Incoming DM stream from %v...", s.Conn().RemotePeer())
		rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
		dmb, err := rw.ReadBytes(byte(0))
		if err != nil {
			log.Errorf("error reading DM data from stream: %v", err)
			return
		}
		dm := DM{}
		json.Unmarshal(dmb, &dm)
		if !did.IsValid(dm.Did) {
			log.Errorf("the DID %s in the DM is not valid", dm.Did)
			return
		}
		did, _ := did.Parse(dm.Did)
		n, err := blockchain.ResolveENS(did.ID.ID, apikey)
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
		DMs = append(DMs, dm)
		log.Infof("direct message from %v: %s", did.ID.ID, dm.Message)
	})
}

func SendDM(ctx context.Context, ipfscore ipfs.IPFSCore, apikey string, _did string, text string) error {
	if !did.IsValid(_did) {
		return fmt.Errorf("the DID %s is not valid", _did)
	}
	d, err := did.Parse(_did)
	if err != nil {
		return fmt.Errorf("could not parse DID %s: %v", _did, err)
	}
	if d.ID.Method != "ens" {
		return fmt.Errorf("only ENS DIDs are supported currently")
	}
	n, err := blockchain.ResolveENS(d.ID.ID, apikey)
	if err != nil {
		return fmt.Errorf("could not resolve ENS name %s: %v", _did, err)
	}
	log.Infof("sending DM to DID %s...", _did)
	pid, err := ipfs.GetIPFSNodeIdentityFromPublicKeyName(n.IPFSPubKey)
	if err != nil {
		return fmt.Errorf("could not get IPFS node identity from string %s: %v", n.IPFSPubKey, err)
	}
	log.Infof("IPFS node identity for %s is %v", _did, pid)
	addr, err := ipfscore.Node.DHTClient.FindPeer(ctx, pid)
	if err != nil {
		peers, _ := ipfscore.Api.PubSub().Peers(ctx, options.PubSub.Topic("flubber"))
		var found bool = false
		for i := range peers {
			if peers[i] == pid {
				found = true
				log.Infof("found peer in peers table for flubber topic")
				break
			}
		}
		if !found {
			return fmt.Errorf("the node %v for DID %s is not online. Authenticated DMs cannot be sent to this DID right now", pid, _did)
		}
	} else {
		log.Infof("the node %v for DID %s is online at address %v", pid, _did, addr)
	}
	s, err := ipfscore.Node.PeerHost.NewStream(ctx, pid, protocol.ID("flubberchat/0.1"))
	if err != nil {
		return fmt.Errorf("could not open new stream to peer %v: %v", pid, err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
	dm := DM{
		Did:     _did,
		Message: text,
		Time:    time.Now(),
	}
	bdm, _ := json.Marshal(dm)
	_, err = rw.Write(append(bdm, byte(0)))
	if err != nil {
		return fmt.Errorf("could not write DM to stream to peer %v: %v", pid, err)
	}
	err = rw.Flush()
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
	return err
}

func EventQueryHandler(s network.Stream) {

}
