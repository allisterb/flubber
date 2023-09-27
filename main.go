package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	logging "github.com/ipfs/go-log/v2"
	"github.com/mbndr/figlet4go"

	"github.com/allisterb/flubber/blockchain"
	"github.com/allisterb/flubber/did"
	"github.com/allisterb/flubber/ipfs"
	"github.com/allisterb/flubber/node"
	"github.com/allisterb/flubber/p2p"
	"github.com/allisterb/flubber/util"
)

type NodeCmd struct {
	Cmd string `arg:"" name:"cmd" help:"The command to run. Can be one of: create."`
	Did string `arg:"" optional:"" name:"did" help:"Use the DID linked to this name."`
}

type DidCmd struct {
	Cmd  string `arg:"" name:"cmd" help:"The command to run. Can be one of: resolve, dm"`
	Name string `arg:"" name:"name" help:"Get the DID linked to this name."`
	Arg  string `arg:"" optional:"" name:"did" help:"Argument for the DID command."`
}

var log = logging.Logger("flubber/main")

var CLI struct {
	Node NodeCmd `cmd:"" help:"Run Flubber node commands."`
	Did  DidCmd  `cmd:"" help:"Run commands on the DID linked to a name."`
}

func init() {
	if os.Getenv("GOLOG_LOG_LEVEL") == "info" { // Reduce noise level of some loggers
		logging.SetLogLevel("dht/RtRefreshManager", "error")
		logging.SetLogLevel("bitswap", "error")
		logging.SetLogLevel("connmgr", "error")
		logging.SetLogLevel("canonical-log", "error")
	} else if os.Getenv("GOLOG_LOG_LEVEL") == "" {
		logging.SetAllLoggers(logging.LevelInfo)
		logging.SetLogLevel("dht/RtRefreshManager", "error")
		logging.SetLogLevel("bitswap", "error")
		logging.SetLogLevel("connmgr", "error")
		logging.SetLogLevel("net/identify", "error")
		logging.SetLogLevel("canonical-log", "error")
	}
}

func main() {
	ascii := figlet4go.NewAsciiRender()
	options := figlet4go.NewRenderOptions()
	options.FontColor = []figlet4go.Color{
		figlet4go.ColorCyan,
		figlet4go.ColorBlue,
		figlet4go.ColorRed,
		figlet4go.ColorYellow,
	}
	renderStr, _ := ascii.RenderOpts("Flubber", options)
	fmt.Print(renderStr)

	ctx := kong.Parse(&CLI)
	ctx.FatalIfErrorf(ctx.Run(&kong.Context{}))
}

func (c *NodeCmd) Run(clictx *kong.Context) error {
	switch strings.ToLower(c.Cmd) {
	case "init":
		if c.Did == "" {
			return fmt.Errorf("you must specify a user DID to initialize the node")
		}
		if !did.IsValid(c.Did) {
			return fmt.Errorf("invalid DID: %s", c.Did)
		}
		d := filepath.Join(util.GetUserHomeDir(), ".flubber")
		if _, err := os.Stat(d); err != nil {
			err := os.Mkdir(d, 0755)
			if err != nil {
				log.Errorf("error creating node configuration directory %s: %v", d, err)
				return err
			}
		}
		f := filepath.Join(d, "node.json")
		if _, err := os.Stat(f); err == nil {
			log.Errorf("node configuration file %s already exists", f)
			return nil
		}
		priv, pub, err := ipfs.GenerateIPFSNodeKeyPair()
		if err != nil {
			return err
		} else {
			ppub, _ := ipfs.GetIPNSPublicKeyName(pub)
			log.Infof("IPFS rsa-2048 public key (ipfsKey): %s", ppub)
		}

		//nssk, _ := nip19.EncodePrivateKey(nsk)
		//nppk, _ := nip19.EncodePublicKey(npk)
		config := node.Config{
			Did:         c.Did,
			IPFSPubKey:  pub,
			IPFSPrivKey: priv,
		}
		data, _ := json.MarshalIndent(config, "", " ")
		err = os.WriteFile(filepath.Join(d, "node.json"), data, 0644)
		if err != nil {
			log.Errorf("error creating node configuration file: %v", err)
			return err
		}
		log.Infof("user DID is %s", c.Did)
		log.Infof("node identity is %s", ipfs.GetIPFSNodeIdentity(pub).Pretty())
		log.Infof("flubber node configuration initialized at %s", filepath.Join(d, "node.json"))
		log.Info("add your Infura and Web3.Storage API secret keys to this file to complete the configuration")
		return nil
	case "run":
		ctx, cancel := context.WithCancel(context.Background())
		err := node.Run(ctx, cancel)
		return err

	default:
		return fmt.Errorf("unknown node command: %s", c.Cmd)
	}
}

func (c *DidCmd) Run(clictx *kong.Context) error {
	switch strings.ToLower(c.Cmd) {

	case "resolve":
		d, err := did.Parse(c.Name)
		if err != nil {
			log.Errorf("could not parse DID %s: %v", c.Name, err)
			return err
		}
		if d.ID.Method != "ens" {
			log.Errorf("only ENS DIDs are supported currently.")
			return nil
		}
		config, err := node.LoadConfig()
		if err != nil {
			log.Error("could not load patr node config")
			return err
		}
		r, err := blockchain.ResolveENS(d.ID.ID, config.InfuraSecretKey)
		if err == nil {
			fmt.Printf("ETH Address: %s\nNostr Public-Key: %v\nIPFS Public-Key: %s\nContent-Hash: %s\nAvatar: %s", r.Address, r.NostrPubKey, r.IPFSPubKey, r.ContentHash, r.Avatar)
			return nil
		} else {
			return err
		}

	case "dm":
		if !did.IsValid(c.Name) {
			return fmt.Errorf("%s is not a valid Patr DID", c.Name)
		}
		d, err := did.Parse(c.Name)
		if err != nil {
			log.Errorf("could not parse DID %s: %v", c.Name, err)
			return err
		}
		config, err := node.LoadConfig()
		if err != nil {
			return fmt.Errorf("could not load patr node config")
		}
		ctx, _ := context.WithCancel(context.Background())
		ipfscore, err := ipfs.StartIPFSNode(ctx, config.IPFSPrivKey, config.IPFSPubKey)
		if err != nil {
			return fmt.Errorf("could not start patr IPFS node")
		}
		err = p2p.SendDM(ctx, *ipfscore, config.InfuraSecretKey, d.ID.ID, c.Arg)
		ipfscore.Shutdown()
		return err

	default:
		return fmt.Errorf("Unknown did command: %s", c.Cmd)
	}
}
