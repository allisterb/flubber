package did

import (
	logging "github.com/ipfs/go-log/v2"
	ssi "github.com/nuts-foundation/go-did"
	godid "github.com/nuts-foundation/go-did/did"
)

var log = logging.Logger("flubber/did")

func Parse(str string) (*godid.Document, error) {
	didID, err := godid.ParseDID(str)
	if err != nil {
		return &godid.Document{}, err
	}
	doc := &godid.Document{
		Context: []ssi.URI{godid.DIDContextV1URI()},
		ID:      *didID,
	}
	return doc, err
}

func IsValid(str string) bool {
	d, err := Parse(str)
	if err != nil {
		log.Errorf("Could not parse DID %s: %v", str, err)
		return false
	} else if d.ID.Method != "ens" {
		log.Errorf("invalid DID: %s. Only ENS DIDs are supported currently", str)
		return false
	} else {
		return true
	}
}
