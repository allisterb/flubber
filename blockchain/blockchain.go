package blockchain

import (
	"fmt"

	"encoding/binary"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	ens "github.com/wealdtech/go-ens/v3"
)

type ENSName struct {
	Address     string
	IPFSPubKey  string
	NostrPubKey string
	ContentHash cid.Cid
	Avatar      string
}

var log = logging.Logger("patr/blockchain")

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
	avatar, err := r.Text("avatar")
	if err != nil {
		log.Warnf("could not resolve avatar text record for ENS name %s: %v", name, err)
	}
	ipfsKey, err := r.Text("ipfsKey")
	if err != nil {
		log.Warnf("could not resolve ipfsKey text record for ENS name %s: %v", name, err)
	}
	nostrKey, err := r.Text("nostrKey")
	if err != nil {
		log.Warnf("could not resolve nostrKey text record for ENS name %s: %v", name, err)
	}

	log.Infof("resolved ENS name %v", name)

	record := ENSName{
		Address:     address.Hex(),
		IPFSPubKey:  ipfsKey,
		NostrPubKey: nostrKey,
		ContentHash: chashcid,
		Avatar:      avatar,
	}

	return record, err
}
