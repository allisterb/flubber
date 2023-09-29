package node

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/wabarc/ipfs-pinner/pkg/pinata"

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
	PinataApiKey    string
	PinataSecretKey string
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
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 1024 * 1024,
	WriteBufferSize: 1024 * 1024 * 1024,
	//Solving cross-domain problems
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

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
	if config.PinataApiKey == "" {
		log.Warnf("Pinata API key not set in configuration file")
		return Config{}, fmt.Errorf("PINATA API KEY NOT SET IN CONFIGURATION FILE")
	}
	if config.PinataSecretKey == "" {
		log.Warnf("Pinata secret key not set in configuration file")
		return Config{}, fmt.Errorf("PINATA SECRET KEY NOT SET IN CONFIGURATION FILE")
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
	r.GET("/did", getDid)
	r.GET("/stream/messages", wsMessages)
	r.PUT("/files", putFiles)

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
		c.String(http.StatusBadRequest, "Subscribe query-string must be in the form Topic=(topic).\n")
		return
	}
	ctx, _ := context.WithDeadline(nodeRun.Ctx, time.Now().Add(time.Duration(5*time.Second)))
	messages, err := ipfs.GetSubscriptionMessages(ctx, nodeRun.Ipfs, topic)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not retrieve messages for topic %s: %v.\n", topic, err)
	} else {
		c.JSON(http.StatusOK, messages)
	}
}

func putDM(c *gin.Context) {
	var dm p2p.DM
	if err := c.BindQuery(&dm); err != nil || dm.Did == "" || dm.Message == "" {
		c.String(http.StatusBadRequest, "Message query-string must be in the form Did=(did)&Message=(message).\n")
		return
	}
	err := p2p.SendDM(nodeRun.Ctx, nodeRun.Ipfs, CurrentConfig.InfuraSecretKey, dm.Did, CurrentConfig.Did, dm.Message)
	//c.String(http.StatusOK, "Sent DM to %s.", dm.Did)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not send DM to %s: %v.\n", dm.Did, err)
	} else {
		c.String(http.StatusOK, "Sent DM to %s.\n", dm.Did)
	}
}

func getDM(c *gin.Context) {
	c.JSON(http.StatusOK, p2p.DMs)
}

func putSubscriptions(c *gin.Context) {
	topic := c.Query("Topic")
	if topic == "" {
		c.String(http.StatusBadRequest, "Subscribe query-string must be in the form Topic=(topic).\n")
		return
	}
	err := ipfs.SubscribeToTopic(nodeRun.Ctx, nodeRun.Ipfs, topic)
	if err != nil {
		log.Errorf("could not subscribe to topic %s: %v", topic, err)
		c.String(http.StatusInternalServerError, "Could not subscribe to topic %s: %v.\n", topic, err)
		return
	} else {
		log.Infof("subscribed to topic %s", topic)
		c.String(http.StatusOK, "Subscribed to topic %s.\n", topic)
		return
	}
}

func getSubscriptions(c *gin.Context) {
	topics, err := ipfs.GetSubscriptionTopics(nodeRun.Ctx, nodeRun.Ipfs)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not retrieve subscription topics :%v.\n", err)
		return
	} else {
		c.JSON(http.StatusOK, topics)
		return
	}
}

func putMessages(c *gin.Context) {
	topic := c.Query("Topic")
	data := c.Query("Data")
	t := c.Query("Type")

	if topic == "" || data == "" {
		c.String(http.StatusBadRequest, "Query-string must be in the form Topic=(topic)&Data=(data)&Type=(type).\n")
		return
	}

	if t == "" {
		t = "string"
	}

	m := ipfs.SubscriptionMessage{
		Did:   CurrentConfig.Did,
		Type:  t,
		Data:  data,
		Time:  time.Now(),
		Topic: topic,
	}

	err := ipfs.PublishSubscriptionMessage(nodeRun.Ctx, nodeRun.Ipfs, topic, m)
	if err != nil {
		log.Errorf("error publishing message: %v", err)
		c.String(http.StatusInternalServerError, "Error publishing message: %v.\n", err)
	} else {
		log.Infof("published message: %v to topic %s", m, topic)
		c.String(http.StatusOK, "Published message: %v to topic %s.", m, topic)
	}
}

func getPeers(c *gin.Context) {
	topic := c.Query("Topic")
	peers, err := ipfs.GetSubscriptionPeers(nodeRun.Ctx, nodeRun.Ipfs, topic)
	if err != nil {
		log.Errorf("error getting peers: %v", err)
		c.String(http.StatusInternalServerError, "Error getting peers: %v.", err)
	} else {
		c.JSON(http.StatusOK, peers)
	}
}

func getDid(c *gin.Context) {
	_did := c.Query("Did")
	if _did == "" {
		c.String(http.StatusBadRequest, "Query-string must be in the form Did=(did).\n")
		return
	}
	d, err := did.Parse(_did)
	if err != nil {
		log.Errorf("error parsing DID %s: %v", _did, err)
		c.String(http.StatusInternalServerError, "Error parsing did %s: %v.", _did, err)
		return
	} else if d.ID.Method != "ens" {
		log.Errorf("error parsing DID %s: only ENS DIDs are currently supported", _did)
		c.String(http.StatusInternalServerError, "Error parsing did %s: only ENS DIDs are currently supported.", _did)
		return
	}
	en, err := blockchain.ResolveENS(d.ID.ID, CurrentConfig.InfuraSecretKey)
	if err != nil {
		log.Errorf("error resolving ENS name %s: %v", d.ID.ID, err)
		c.String(http.StatusInternalServerError, "error resolving ENS name %s: %v.", d.ID.ID, err)
		return
	} else {
		en.IPFSPubKey, _ = blockchain.ConvertIpfsKeyToPeer(en.IPFSPubKey)
		c.JSON(http.StatusOK, en)
		return
	}
}

func wsMessages(c *gin.Context) {
	topic := c.Query("Topic")
	if topic == "" {
		c.String(http.StatusBadRequest, "Query-string must be in the form Topic=(topic).\n")
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Errorf("error creating WebSocket connection: %v", err)
		c.String(http.StatusInternalServerError, "Error creating WebSocket connection: %v.", err)
		return
	}
	defer conn.Close()
	s, err := nodeRun.Ipfs.Api.PubSub().Subscribe(nodeRun.Ctx, topic)
	if err != nil {
		log.Errorf("error creating IPFS pubsub subscription: %v", err)
		c.String(http.StatusInternalServerError, "Error creating IPFS pubsub subscription: %v.", err)
		return
	}
	defer s.Close()
	log.Infof("streaming message topic %s to %v at %v", topic, c.Request.UserAgent(), c.Request.RemoteAddr)
	for {
		_m, err := s.Next(nodeRun.Ctx)
		if err == io.EOF {
			time.Sleep(time.Millisecond * 100)
			continue
		}
		if err == context.DeadlineExceeded || err == context.Canceled {
			log.Infof("%v, closing connection", err)
			break
		} else if err != nil {
			log.Errorf("error retrieving message for subscription %s: %v", topic, err)
			continue
		} else {
			buf := bytes.NewBuffer(_m.Data())
			var m ipfs.SubscriptionMessage
			dec := gob.NewDecoder(buf)
			err = dec.Decode(&m)
			if err != nil {
				log.Errorf("error decoding message: %v", err)
				continue
			} else {
				log.Infof("received message for topic %s: %v", topic, m)
				conn.WriteJSON(m)
			}
		}
	}
}

func putFiles(c *gin.Context) {
	f := c.Query("Path")
	if f == "" {
		c.String(http.StatusBadRequest, "Query-string must be in the form Path=(path).\n")
		return
	}
	b, err := os.ReadFile(f)
	if err != nil {
		log.Errorf("error reading file %s: %v", f, err)
		c.String(http.StatusInternalServerError, "Error reading file %s: %v", f, err)
		return
	}
	ci_, err := nodeRun.Ipfs.PutIPFSBlock(nodeRun.Ctx, b)
	if err != nil {
		log.Errorf("coud not store file %s: %v", f, err)
		c.String(http.StatusInternalServerError, "coud not store file %s: %v", f, err)
		return
	}
	pnt := pinata.Pinata{Apikey: CurrentConfig.PinataApiKey, Secret: CurrentConfig.PinataSecretKey}
	s, err := pnt.PinFile(f)
	if err != nil {
		log.Errorf("coud not store file %s: %v", f, err)
		c.String(http.StatusInternalServerError, "coud not store file %s: %v", f, err)
		return
	}
	pcid, _ := cid.Parse(s)

	m := ipfs.SubscriptionMessage{
		Did:   CurrentConfig.Did,
		Type:  "cid",
		Data:  pcid.String(),
		Time:  time.Now(),
		Topic: "files",
	}
	err = ipfs.PublishSubscriptionMessage(nodeRun.Ctx, nodeRun.Ipfs, "files", m)
	if err != nil {
		log.Errorf("error publishing message: %v", err)
		c.String(http.StatusInternalServerError, "Error publishing file notification message: %v.\n", err)
	} else {
		log.Infof("published message: %v to topic %s", m, "files")
		c.String(http.StatusOK, "put file %s to IPFS block /ipfs/%v and Pinata pin CID %v", f, ci_, pcid)
	}
}
