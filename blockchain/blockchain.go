package blockchain

import (
	"fmt"

	"encoding/binary"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	ens "github.com/wealdtech/go-ens/v3"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multibase"
)

type ENSName struct {
	Address     string
	IPFSPubKey  string
	ContentHash cid.Cid
}

var log = logging.Logger("flubber/blockchain")

func ResolveENS(name string, apikey string) (ENSName, error) {
	if apikey == "" {
		return ENSName{}, fmt.Errorf("The Infura API secret key was not specified")
	}
	log.Infof("resolving ENS name %v...", name)
	client, err := ethclient.Dial(fmt.Sprintf("https://mainnet.infura.io/v3/%s", apikey))
	if err != nil {
		log.Errorf("could not create Infura Ethereum API client: %v", err)
		return ENSName{}, err
	}
	r, err := ens.NewResolver(client, name)
	if err != nil {
		log.Errorf("could not create resolver ENS name %s: %v", name, err)
		return ENSName{}, err
	}
	address, err := r.Address()
	if err != nil {
		log.Errorf("could not resolve address for ENS name %s: %v", name, err)
		return ENSName{}, err
	}
	chash, err := r.Contenthash()
	if err != nil {
		log.Errorf("could not resolve content hash record for ENS name %s: %v", name, err)
		return ENSName{}, err
	}
	chashcid := cid.Cid{}
	if chash != nil && binary.Size(chash) > 0 && chash[0] == 0xe3 {
		_, chashcid, err = cid.CidFromBytes(chash[2:])
		if err != nil {
			log.Errorf("could not decode IPFS content hash as CID: %v", err)
		}
	}
	ipfsKey, err := r.Text("ipfsKey")
	if err != nil {
		log.Errorf("could not resolve ipfsKey text record for ENS name %s: %v", name, err)
		return ENSName{}, err
	}

	record := ENSName{
		Address:     address.Hex(),
		IPFSPubKey:  ipfsKey,
		ContentHash: chashcid,
	}

	return record, err
}

func ConvertIpfsKeyToPeer(ipfsKey string) (string, error) {
	_, b, err := multibase.Decode(ipfsKey)
	if err != nil {
		return "", fmt.Errorf("could not decode %s as a multibase string: %v", ipfsKey, err)
	}
	_, c, err := cid.CidFromBytes(b)
	if err != nil {
		return "", fmt.Errorf("could not decode %s as a CID: %v", ipfsKey, err)
	}
	id, err := peer.FromCid(c)
	if err != nil {
		return "", fmt.Errorf("could not get peer ID from CID %v : %v", c, err)
	}
	return id.Pretty(), err
}
