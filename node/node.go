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
	Ctx  context.Context
	Ipfs ipfs.IPFSCore
}

var log = logging.Logger("flubber/node")

var CurrentConfig = Config{}
var CurrentConfigInitialized = false
var nodeRun = NodeRun{}

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

func Run(ctx context.Context, cancel context.CancelFunc) error {
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

	gin.SetMode(gin.DebugMode)
	r := gin.New()
	r.Use(ginzap.Ginzap(log.Desugar(), time.RFC3339Nano, true))
	r.Use(ginzap.RecoveryWithZap(log.Desugar(), true))

	r.GET("/DM", getDM)
	r.PUT("/DM", putDM)
	r.GET("/messages", getMessages)
	r.PUT("/messages", putMessages)
	r.GET("/subscriptions", getSubscriptions)
	r.PUT("/subscriptions", putSubscriptions)
	r.GET("/peers", getPeers)

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

	nodeRun = NodeRun{
		Ctx:  ctx,
		Ipfs: *ipfs,
	}

	log.Info("Flubber node started, press Ctrl-C to stop...")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	cancel()
	ipfs.Shutdown()
	srv.Shutdown(ctx)

	return err
}

func getMessages(c *gin.Context) {
	topic := c.Query("Topic")
	if topic == "" {
		c.String(http.StatusBadRequest, "Subscribe query-string must be in the form Topic=(topic)")
		return
	}
	ctx, _ := context.WithDeadline(nodeRun.Ctx, time.Now().Add(time.Duration(5*time.Second)))
	messages, err := ipfs.GetSubscriptionMessages(ctx, nodeRun.Ipfs, topic)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not retrieve messages for topic %s: %v", topic, err)
	} else {
		c.JSON(http.StatusOK, messages)
	}
}

func putDM(c *gin.Context) {
	var dm p2p.DM
	if err := c.BindQuery(&dm); err != nil || dm.Did == "" || dm.Content == "" {
		c.String(http.StatusBadRequest, "Message query-string must be in the form Did=(did)&Content=(content).")
		return
	}
	err := p2p.SendDM(nodeRun.Ctx, nodeRun.Ipfs, CurrentConfig.InfuraSecretKey, dm.Did, dm.Content)
	//c.String(http.StatusOK, "Sent DM to %s.", dm.Did)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not send DM to %s: %v", dm.Did, err)
	} else {
		c.String(http.StatusOK, "Sent DM to %s.", dm.Did)
	}
}

func getDM(c *gin.Context) {
	c.JSON(http.StatusOK, p2p.DMs)

}

func putSubscriptions(c *gin.Context) {
	topic := c.Query("Topic")
	if topic == "" {
		c.String(http.StatusBadRequest, "Subscribe query-string must be in the form Topic=(topic)")
		return
	}
	err := ipfs.SubscribeToTopic(nodeRun.Ctx, nodeRun.Ipfs, topic)
	if err != nil {
		log.Errorf("could not subscribe to topic %s: %v", topic, err)
		c.String(http.StatusInternalServerError, "could not subscribe to topic %s: %v", topic, err)
		return
	} else {
		log.Infof("subscribed to topic %s", topic)
		c.String(http.StatusOK, "subscribed to topic %s", topic)
		return
	}
}

func getSubscriptions(c *gin.Context) {
	topics, err := ipfs.GetSubscriptionTopics(nodeRun.Ctx, nodeRun.Ipfs)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not retrieve subscription topics :%v", err)
		return
	} else {
		c.JSON(http.StatusOK, topics)
		return
	}
}

func putMessages(c *gin.Context) {
	topic := c.Query("Topic")
	message := c.Query("Message")
	if topic == "" || message == "" {
		c.String(http.StatusBadRequest, "query-string must be in the form Topic=(topic)&Message=(message)")
		return
	}

	m := ipfs.SubscriptionMessage{
		Did:     CurrentConfig.Did,
		Content: message,
		Time:    time.Now(),
		Topic:   topic,
	}

	err := ipfs.PublishSubscriptionMessage(nodeRun.Ctx, nodeRun.Ipfs, topic, m)
	if err != nil {
		log.Errorf("error publishing message: %v", err)
		c.String(http.StatusInternalServerError, "error publishing message: %v", err)
	} else {
		log.Infof("published message: %v to topic %s", m, topic)
		c.String(http.StatusOK, "published message: %v", m)
	}
}

func getPeers(c *gin.Context) {
	topic := c.Query("Topic")
	peers, err := ipfs.GetPeers(nodeRun.Ctx, nodeRun.Ipfs, topic)
	if err != nil {
		log.Errorf("error getting peers: %v", err)
		c.String(http.StatusInternalServerError, "error getting: %v", err)
	} else {
		c.JSON(http.StatusOK, peers)
	}
}
