package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	logging "github.com/ipfs/go-log/v2"

	"github.com/allisterb/flubber/blockchain"
	"github.com/allisterb/flubber/did"
	"github.com/allisterb/flubber/ipfs"
	"github.com/allisterb/flubber/p2p"
	"github.com/allisterb/flubber/util"
)

type Config struct {
	Did             string
	IPFSPubKey      []byte
	IPFSPrivKey     []byte
	InfuraSecretKey string
	W3SSecretKey    string
	IPNSKeys        map[byte]byte
}

type NodeRun struct {
	Ctx    context.Context
	Config Config
	Ipfs   ipfs.IPFSCore
}

var log = logging.Logger("flubber/node")

var CurrentConfig = Config{}
var CurrentConfigInitialized = false

func PanicIfNotInitialized() {
	if !CurrentConfigInitialized {
		panic("node configuration is not initialized")
	}
}
func LoadConfig() (Config, error) {
	f := filepath.Join(filepath.Join(util.GetUserHomeDir(), ".flubber"), "node.json")
	if _, err := os.Stat(f); err != nil {
		log.Errorf("could not find node configuration file %s", f)
		return Config{}, err
	}
	c, err := os.ReadFile(f)
	if err != nil {
		log.Errorf("could not read data from node configuration file: %v", err)
		return Config{}, err
	}
	var config Config
	if err = json.Unmarshal(c, &config); err != nil {
		log.Errorf("could not read JSON data from node configuration file: %v", err)
		return Config{}, err
	}
	if config.IPFSPrivKey == nil || config.IPFSPubKey == nil {
		log.Errorf("IPFS node private or public key not set in configuration file")
		return Config{}, fmt.Errorf("IPFS NODE PRIVATE OR PUBLIC KEY NOT SET IN CONFIGURATION FILE")
	}
	if config.InfuraSecretKey == "" {
		log.Warnf("Infura API secret key not set in configuration file")
		return Config{}, fmt.Errorf("INFURA API SECRET KEY NOT SET IN CONFIGURATION FILE")
	}
	if config.W3SSecretKey == "" {
		log.Warnf("Web3.Storage API secret key not set in configuration file")
		return Config{}, fmt.Errorf("WEB3.STORAGE API SECRET KEY NOT SET IN CONFIGURATION FILE")
	}
	CurrentConfig = config
	CurrentConfigInitialized = true
	return config, nil
}

func Run(ctx context.Context) error {
	_, err := LoadConfig()
	if err != nil {
		return err
	}
	ddd, _ := did.Parse(CurrentConfig.Did)
	_, err = blockchain.ResolveENS(ddd.ID.ID, CurrentConfig.InfuraSecretKey)
	if err != nil {
		return err
	}
	log.Info("starting Flubber node...")
	ipfs, err := ipfs.StartIPFSNode(ctx, CurrentConfig.IPFSPrivKey, CurrentConfig.IPFSPubKey)
	if err != nil {
		log.Errorf("error starting IPFS node: %v", err)
		return err
	}
	ipfs.W3S.SetAuthToken(CurrentConfig.W3SSecretKey)
	//c, _ := cid.NewPrefixV1(cid.Raw, mh.SHA2_256).Sum([]byte("patr"))
	//tctx, _ := context.WithTimeout(ctx, time.Second*10)
	//if err := ipfs.Node.DHTClient.Provide(tctx, c, true); err != nil {
	//	log.Errorf("could not provide patr topic: %v", err)
	//}
	p2p.SetDMStreamHandler(*ipfs, CurrentConfig.InfuraSecretKey)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.GET("/reports", func(c *gin.Context) {
		//allFilter := func(d interface{}) (bool, error) { return true, nil }
		c.JSON(http.StatusOK, "ll")
	})
	r.Use(ginzap.Ginzap(log.Desugar(), time.RFC3339Nano, true))
	r.Use(ginzap.RecoveryWithZap(log.Desugar(), true))
	srv := &http.Server{
		Addr:    ":4242",
		Handler: r,
	}

	go func() {
		log.Infof("starting REST server on %s...", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Infof("REST server shutdown requested: %s", err)
		}
	}()

	log.Info("Flubber node started, press Ctrl-C to stop...")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	ipfs.Shutdown()
	srv.Shutdown(ctx)
	return err
}
