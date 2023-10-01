# About
Flubber provides a language-agnostic API for creating a peer-peer authenticated messaging and data storage fabric using libp2p and IPFS. Flubber embeds an IPFS node and runs as a REST and WebSocket server and provides a simple-to-use API for the following tasks

* Authentication using [DIDs](https://en.wikipedia.org/wiki/Decentralized_identifier) 
* Authenticated direct messaging between nodes
* Pub-sub message queues
* Binary and linked data storage and retrieval
* Using IPFS data pinning services like Pinata

The goal of Flubber is to make it easy for developers across all languages and platforms to use the peer-peer communication abilities of libp2p and IPFS in their own apps and services. Flubber can be used to to add peer-peer communication to apps apps or as a low-level service API for building enterprise or commercial peer-peer communication and data storage services, without having to wrangle with integration of the native libp2p and IPFS libraries or building out a server around the Kubo RPC API. 

# Building

Requires Go 1.19+

1. Clone the repo: `git clone https://github.com/allisterb/flubber.git`
2. Run `go build .` in the repo directory

# Running
### Requirements
* [ENS](https://ens.domains/) domain name - you node will run under this identity
* [Infura API](https://www.infura.io/product/ethereum) seceret key
* [Pinata](https://www.pinata.cloud/) API secret key

### Steps
1. Run `flubber node init` in the repo directory to create the Flubber node configuration file at `$HOME/.flubber/node.json` or `%USERPROFILE%\.flubber\node.json. This file will contain the IPFS identity the node will run under.
2. In the ENS domain name you want to use with Flubber add a text record called `ipfsKey` with the IPFS public key from your node configuration.
3. Set the Did field in your node configurations to your ENS domain name
4. Set the Infura and Pinata API and secret keys in your node configuration.
5. Run `flubber node run` to start the node.
