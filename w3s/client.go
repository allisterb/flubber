package w3s

import (
	"context"
	"fmt"
	"io"
	"net/http"

	ipns_pb "github.com/ipfs/boxo/ipns/pb"
	"github.com/ipfs/go-cid"
)

// Web3Response is a response to a call to the Get method.
type Web3Response struct {
	*http.Response
}

const clientName = "web3.storage/go"

// Client is a HTTP API client to the web3.storage service.
type Client interface {
	Get(context.Context, cid.Cid) (*Web3Response, error)
	//Put(context.Context, fs.File, ...PutOption) (cid.Cid, error)
	PutCar(context.Context, io.Reader) (cid.Cid, error)
	Status(context.Context, cid.Cid) (*Status, error)
	//List(context.Context, ...ListOption) (*UploadIterator, error)
	Pin(context.Context, cid.Cid, ...PinOption) (*PinResponse, error)
	GetName(context.Context, string) (*ipns_pb.IpnsEntry, error)
	PutName(context.Context, *ipns_pb.IpnsEntry, string) error
	GetAuthToken() string
	SetAuthToken(string)
}

type clientConfig struct {
	token    string
	endpoint string
	hc       *http.Client
}

type client struct {
	cfg *clientConfig
}

func (c *client) Get(ctx context.Context, cid cid.Cid) (*Web3Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/car/%s", c.cfg.endpoint, cid), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.cfg.token))
	req.Header.Add("X-Client", clientName)
	res, err := c.cfg.hc.Do(req)
	return &Web3Response{Response: res}, err
}

func (c *client) GetAuthToken() string {
	return c.cfg.token
}

func (c *client) SetAuthToken(token string) {
	c.cfg.token = token
}

// NewClient creates a new web3.storage API client.
func NewClient(options ...Option) (Client, error) {
	cfg := clientConfig{
		endpoint: "https://api.web3.storage",
		hc:       &http.Client{},
	}
	for _, opt := range options {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.token == "" {
		return nil, fmt.Errorf("missing auth token")
	}
	c := client{cfg: &cfg}
	return &c, nil
}

var _ Client = (*client)(nil)
